package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/pkg/errors"
	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/run/metrics"
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
		targeter := NewJSONRPCTargeter(cfg.RpcUrl, body)

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
