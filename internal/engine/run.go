package engine

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/pkg/errors"
	vegeta "github.com/tsenart/vegeta/v12/lib"

	types "github.com/stellar/stellar-rpc-blaster/internal/config"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/metrics"
)

type endpointBlast struct {
	EndpointBlastConfig EndpointBlastConfig
	BlastPacer          RampToConstantPacer
}

// Entry/exit point from app.go
// RunVegeta runs a load test using Vegeta using the config settings through the LoadTestSettings interface
// Sets up shared HTTP client, constructs per-endpoint blast configs, and fires off the blasts asynchronously
func RunVegeta(ctx context.Context, cfg types.LoadTestSettings, out chan<- blasterMetrics.Sample) error {
	// have duration + grace period for in-flight requests as timeout to avoid hanging
	ctx, cancel := context.WithTimeout(ctx, cfg.GetDuration())
	defer cancel()

	// Shared HTTP client with connection pooling to prevent ephemeral port exhaustion
	httpClient, limits := SharedHTTPClient()

	newBlaster := func() *vegeta.Attacker {
		return NewBlasterWithClient(httpClient, limits)
	}

	// Construct endpoint blast configs
	// 3... 2... 1...
	var endpointBlasts []endpointBlast
	for _, endpointKey := range cfg.GetEndpoints() {
		rps := cfg.GetEndpointRPS(endpointKey)

		// Form request and Vegeta targeter from that
		request := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  endpointKey,
			"params":  map[string]any{}, // optional -- to be used when we do PR 573/data-dependent endpoints
		}
		body, err := json.Marshal(request)
		if err != nil {
			return errors.Wrap(err, "error marshalling JSON request")
		}
		targeter := NewJSONRPCTargeter(cfg.GetRpcUrl(), body)

		endpointBlasts = append(endpointBlasts, endpointBlast{
			EndpointBlastConfig: EndpointBlastConfig{
				EndpointKey: endpointKey,
				RPS:         rps,
				Targeter:    targeter,
			},
			BlastPacer: RampToConstantPacer{
				TotalDuration: cfg.GetDuration(),
				RampDuration:  cfg.GetRampUp(),
				StartRPS:      1,
				MaxRPS:        rps,
			},
		})
	}

	// Fire!
	var wg sync.WaitGroup
	for _, blast := range endpointBlasts {
		wg.Add(1)
		go func(eb endpointBlast) {
			defer wg.Done()
			blastAtEndpoint(ctx, eb.EndpointBlastConfig, eb.BlastPacer, newBlaster, out)
		}(blast)
	}
	wg.Wait()
	return nil
}
