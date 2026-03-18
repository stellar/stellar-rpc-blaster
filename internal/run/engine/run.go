package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/run/metrics"
	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters"
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
) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	newBlaster := func() *vegeta.Attacker {
		return NewBlasterWithClient(httpClient, util.MaxWorkers)
	}

	// Pre-load seed parameters once for all data-dependent endpoints
	var sharedParams *parameters.Parameters
	if cfg.InputDataPath != "" {
		p, err := parameters.GetParameters(cfg.InputDataPath)
		if err != nil {
			return fmt.Errorf("failed to load seed data: %w", err)
		}
		sharedParams = p

		// Run preflight validation to ensure seed data is fresh enough for the target RPC before blasting any endpoints
		if err := sharedParams.ValidateConfiguredEndpoints(ctx, cfg.RpcClient, cfg.GetActiveEndpoints()); err != nil {
			return fmt.Errorf("preflight seed validation failed (try rerunning `generate`): %w", err)
		}
	}

	// Construct endpoint blast configs
	// 3... 2... 1...
	var endpointBlasts []endpointBlast
	for _, endpointKey := range cfg.GetEndpoints() {
		rps := cfg.GetEndpointRPS(endpointKey)
		if rps <= 0 {
			logger.Infof("Skipping endpoint: %v (RPS <= 0)", endpointKey)
			continue
		}

		paramMaps, err := parameters.BuildEndpointParams(endpointKey, sharedParams)
		if err != nil {
			return fmt.Errorf("couldn't build params for endpoint %s: %w", endpointKey, err)
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
				return fmt.Errorf("couldn't marshal request for endpoint %s: %w", endpointKey, err)
			}
		}
		targeter := NewJSONRPCTargeter(cfg.RpcUrl, bodies)
		if len(bodies) > 1 {
			logger.Infof("Endpoint %s: rotating through %d parameterized request bodies", endpointKey, len(bodies))
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
