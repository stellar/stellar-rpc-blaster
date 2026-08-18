package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/run/metrics"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/run/parameters"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

type BlastEngine struct {
	BlastSpecs []EndpointBlastConfig
	blastFn    func() *vegeta.Attacker
	outCh      chan<- blasterMetrics.Sample
	cfg        config.Config
}

type EndpointBlastConfig struct {
	EndpointKey string
	Targeter    vegeta.Targeter
	BlastPacer  SteppedPacer
}

// Entry/exit point from app.go
// NewBlastEngine creates a new BlastEngine instance with the given configuration and HTTP client
func NewBlastEngine(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	httpClient *http.Client,
	out chan<- blasterMetrics.Sample,
) (*BlastEngine, error) {
	newBlaster := func() *vegeta.Attacker {
		return NewBlasterWithClient(httpClient, util.MaxWorkers)
	}

	// Pre-load seed parameters once for all data-dependent endpoints
	var sharedParams *parameters.Parameters
	if cfg.InputDataPath != "" {
		p, err := parameters.GetParameters(cfg.InputDataPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load seed data: %w", err)
		}
		sharedParams = p

		// Run preflight validation to ensure seed data is fresh enough for the target RPC before blasting any endpoints
		if err := sharedParams.ValidateConfiguredEndpoints(ctx, cfg.RpcClient, cfg.GetActiveEndpoints()); err != nil {
			return nil, fmt.Errorf("preflight seed validation failed (try rerunning `generate`): %w", err)
		}
		for _, warning := range sharedParams.Warnings(cfg.GetActiveEndpoints()) {
			logger.Warn(warning)
		}
	}

	// Construct each endpoint's blast config
	var endpointBlasts []EndpointBlastConfig
	for _, endpointKey := range cfg.GetActiveEndpoints() {
		pacer := NewSteppedPacer(endpointKey, cfg)

		maxNumBodies := pacer.Hits(cfg.Duration) // upper limit of how many request bodies we could possibly need
		paramMaps, err := parameters.BuildEndpointParams(endpointKey, int(maxNumBodies), sharedParams, cfg.GetEndpointLimit(endpointKey))
		if err != nil {
			return nil, fmt.Errorf("couldn't build params for endpoint %s: %w", endpointKey, err)
		}
		bodies := make([][]byte, len(paramMaps))
		for i, p := range paramMaps {
			req := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  endpointKey,
				"params":  p,
			}
			bodies[i], err = json.Marshal(req)
			if err != nil {
				return nil, fmt.Errorf("couldn't marshal request for endpoint %s: %w", endpointKey, err)
			}
		}
		targeter := NewJSONRPCTargeter(cfg.RpcUrl, bodies)
		if len(bodies) > 1 {
			logger.Infof("Endpoint %s: rotating through %d parameterized request bodies", endpointKey, len(bodies))
		}

		endpointBlasts = append(endpointBlasts, EndpointBlastConfig{
			EndpointKey: endpointKey,
			Targeter:    targeter,
			BlastPacer:  pacer,
		})
	}
	return &BlastEngine{
		BlastSpecs: endpointBlasts,
		blastFn:    newBlaster,
		outCh:      out,
		cfg:        cfg,
	}, nil
}

// Run executes the blast according to the engine's configuration, sending results to the OutCh for aggregation
func (b *BlastEngine) Run(ctx context.Context, logger *log.Entry, aggregator *blasterMetrics.Aggregator) {
	if b.cfg.Serial {
		for i, blast := range b.BlastSpecs {
			if ctx.Err() != nil {
				logger.Errorf("Endpoint blasts terminating early due to context error: %s", ctx.Err().Error())
				return
			}
			// Cool off between endpoints so a struggling server can recover before the next blast
			if i > 0 && b.cfg.Cooloff > 0 {
				logger.Infof("Cooling off for %s before next endpoint", b.cfg.Cooloff)
				select {
				case <-time.After(b.cfg.Cooloff):
				case <-ctx.Done():
					logger.Errorf("Endpoint blasts terminating early during cooloff: %s", ctx.Err().Error())
					return
				}
			}
			aggregator.ActivateEndpoint(blast.EndpointKey)
			logger.Infof("Serial mode: starting endpoint %s", blast.EndpointKey)
			logger.Infof("%s", aggregator) // print progress with new endpoint activated

			epCtx, epCancel := context.WithTimeout(ctx, b.cfg.Duration)
			blastAtEndpoint(epCtx, blast, b.blastFn, b.outCh)
			epCancel()
		}
	} else {
		logger.Infof("%s", aggregator) // print initial 0-sample stats

		ctx, cancel := context.WithTimeout(ctx, b.cfg.Duration)
		defer cancel()
		var wg sync.WaitGroup
		for _, blast := range b.BlastSpecs {
			wg.Go(func() {
				blastAtEndpoint(ctx, blast, b.blastFn, b.outCh)
			})
		}
		wg.Wait()
	}
}
