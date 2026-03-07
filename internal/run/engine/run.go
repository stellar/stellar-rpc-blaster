package engine

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/run/metrics"
	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters"
	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters/tx"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type endpointBlast struct {
	EndpointBlastConfig EndpointBlastConfig
	BlastPacer          RampToConstantPacer
}

// Entry/exit point from app.go
// RunVegeta runs a load test using Vegeta using the config settings through the LoadTestSettings interface
// Sets up shared HTTP client, constructs per-endpoint blast configs, and fires off the blasts asynchronously
func RunVegeta(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	httpClient *http.Client,
	out chan<- blasterMetrics.Sample,
	startAggregator func(),
) error {
	newBlaster := func() *vegeta.Attacker {
		return NewBlasterWithClient(httpClient, util.MaxWorkers)
	}

	// Pre-load seed parameters once for all data-dependent endpoints
	var sharedParams *parameters.Parameters
	if cfg.InputDataPath != "" {
		p, err := parameters.GetParameters(cfg.InputDataPath)
		if err != nil {
			return fmt.Errorf("failed to load seed data: %v", err)
		}
		sharedParams = p
	}

	var activeEndpoints, skippedEndpoints []string
	for _, endpointKey := range cfg.GetEndpoints() {
		if cfg.GetEndpointRPS(endpointKey) <= 0 {
			skippedEndpoints = append(skippedEndpoints, endpointKey)
		} else {
			activeEndpoints = append(activeEndpoints, endpointKey)
		}
	}
	if len(skippedEndpoints) > 0 {
		logger.Infof("Skipping endpoints with RPS<=0: %v", strings.Join(skippedEndpoints, ", "))
	}

	// Construct endpoint blast configs
	// 3... 2... 1...
	var endpointBlasts []endpointBlast
	for _, endpointKey := range activeEndpoints {
		rps := cfg.GetEndpointRPS(endpointKey)

		var targeter vegeta.Targeter
		if endpointKey == "sendTransaction" {
			numAccounts := uint32(min(rps, 100)) // cap # of accounts to lessen friendbot calls and account creation

			logger.Warn("sendTransaction causes greater load on the RPC server than other endpoints!")
			logger.Infof("Creating + funding %d on-chain accounts for the sendTransaction endpoint...", numAccounts)
			pool, err := tx.NewTestnetAccountPool(ctx, cfg.RpcClient, numAccounts) // create pool of accounts
			if err != nil {
				return fmt.Errorf("failed to create account pool for sendTransaction: %v", err)
			}
			// Need custom targeter to generate unique (tx, sequence) params for each request
			targeter = NewSendTxTargeter(cfg.RpcUrl, pool, cfg.NetworkPassphrase)
		} else {
			paramMaps, err := parameters.BuildEndpointParams(endpointKey, sharedParams)
			if err != nil {
				return fmt.Errorf("couldn't build params for endpoint %s: %v", endpointKey, err)
			}
			bodies := make([][]byte, len(paramMaps))
			for i, p := range paramMaps {
				bodies[i], err = util.MarshalJsonRpcRequest(endpointKey, p)
				if err != nil {
					return fmt.Errorf("couldn't marshal request for endpoint %s: %v", endpointKey, err)
				}
			}
			targeter = NewJSONRPCTargeter(cfg.RpcUrl, bodies)
			if len(bodies) > 1 {
				logger.Infof("Endpoint %s: rotating through %d parameterized request bodies", endpointKey, len(bodies))
			}
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

	// Start the blast duration clock only after all setup is done
	ctx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	// Start the aggregator
	startAggregator()

	// Fire!
	var wg sync.WaitGroup
	for _, blast := range endpointBlasts {
		wg.Go(func() {
			blastAtEndpoint(ctx, blast.EndpointBlastConfig, blast.BlastPacer, newBlaster, out)
		})
	}
	wg.Wait()
	return nil
}
