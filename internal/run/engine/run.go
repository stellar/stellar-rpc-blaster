package engine

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/run/metrics"
	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters"
	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters/tx"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type BlastEngine struct {
	BlastSpecs        []endpointBlast
	AggregatorStartFn func()
	BlastFn           func() *vegeta.Attacker
	TxAccountPool     *tx.AccountPool
	OutCh             chan<- blasterMetrics.Sample
	Config            config.Config
}

type endpointBlast struct {
	EndpointBlastConfig EndpointBlastConfig
	BlastPacer          RampToConstantPacer
}

// Sets up shared HTTP client, constructs per-endpoint blast configs or targeters, and initializes the blast engine
func NewBlastEngine(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	httpClient *http.Client,
	out chan<- blasterMetrics.Sample,
	startAggregator func(),
) (*BlastEngine, error) {
	newBlaster := func() *vegeta.Attacker {
		return NewBlasterWithClient(httpClient, util.MaxWorkers)
	}

	// Pre-load seed parameters once for all data-dependent endpoints
	var sharedParams *parameters.Parameters
	if cfg.InputDataPath != "" {
		p, err := parameters.GetParameters(cfg.InputDataPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load seed data: %v", err)
		}
		sharedParams = p
		sharedParams.NetworkPassphrase = cfg.NetworkPassphrase
	}

	// Construct endpoint blast configs
	var endpointBlasts []endpointBlast
	var ap *tx.AccountPool
	for _, endpointKey := range cfg.GetEndpoints() {
		rps := cfg.GetEndpointRPS(endpointKey)
		if rps <= 0 {
			logger.Infof("Skipping endpoint: %v (RPS <= 0)", endpointKey)
			continue
		}

		var targeter vegeta.Targeter
		if endpointKey == "sendTransaction" {
			numAccounts := uint32(min(rps, 100)) // cap # of accounts to lessen friendbot calls and account creation

			logger.Warn("sendTransaction causes greater load on the RPC server than other endpoints!")
			logger.Infof("Creating + funding %d on-chain accounts for the sendTransaction endpoint...", numAccounts)
			var err error
			ap, err = tx.NewAccountPool(ctx, cfg.RpcClient, cfg.OriginAccount, numAccounts) // create pool of accounts
			if err != nil {
				return nil, fmt.Errorf("failed to create account pool for sendTransaction: %v", err)
			}

			// Need custom targeter to generate unique (tx, sequence) params for each request
			targeter = NewSendTxTargeter(cfg.RpcUrl, ap, cfg.NetworkPassphrase)
		} else {
			bodies, err := buildBodies(endpointKey, sharedParams)
			if err != nil {
				return nil, err
			}
			if len(bodies) > 1 {
				logger.Infof("Endpoint %s: rotating through %d parameterized request bodies", endpointKey, len(bodies))
			}
			targeter = NewJSONRPCTargeter(cfg.RpcUrl, bodies)
		}

		endpointBlasts = append(endpointBlasts, endpointBlast{
			EndpointBlastConfig: EndpointBlastConfig{
				EndpointKey: endpointKey,
				RPS:         rps,
				Targeter:    targeter,
			},
			BlastPacer: RampToConstantPacer{
				TotalDuration: cfg.Duration,
				RampDuration:  cfg.RampUp,
				StartRPS:      1,
				MaxRPS:        rps,
			},
		})
	}
	return &BlastEngine{
		BlastSpecs:        endpointBlasts,
		AggregatorStartFn: startAggregator,
		BlastFn:           newBlaster,
		TxAccountPool:     ap,
		OutCh:             out,
		Config:            cfg,
	}, nil
}

func buildBodies(endpointKey string, sharedParams *parameters.Parameters) ([][]byte, error) {
	paramMaps, err := parameters.BuildEndpointParams(endpointKey, sharedParams)
	if err != nil {
		return nil, fmt.Errorf("couldn't build params for endpoint %s: %v", endpointKey, err)
	}
	bodies := make([][]byte, len(paramMaps))
	for i, p := range paramMaps {
		bodies[i], err = util.MarshalJsonRpcRequest(endpointKey, p)
		if err != nil {
			return nil, fmt.Errorf("couldn't marshal request for endpoint %s: %v", endpointKey, err)
		}
	}
	return bodies, nil
}

func (b *BlastEngine) Close(ctx context.Context, logger *log.Entry) error {
	if b.TxAccountPool != nil {
		if err := b.TxAccountPool.Close(ctx, b.Config.RpcClient, logger); err != nil {
			return fmt.Errorf("failed to close blast engine: %v", err)
		}
	}
	return nil
}

// Runs a load test using Vegeta using the config settings through the BlastEngine struct
func (b *BlastEngine) FireBlasts(ctx context.Context) error {
	// Start the blast duration clock only after all setup is done
	ctx, cancel := context.WithTimeout(ctx, b.Config.Duration)
	defer cancel()

	// Start the aggregator
	b.AggregatorStartFn()

	// Fire!
	var wg sync.WaitGroup
	for _, blast := range b.BlastSpecs {
		wg.Go(func() {
			blastAtEndpoint(ctx, blast.EndpointBlastConfig, blast.BlastPacer, b.BlastFn, b.OutCh)
		})
	}
	wg.Wait()
	close(b.OutCh)
	return nil
}
