package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/metrics"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type endpointBlast struct {
	EndpointBlastConfig
	BlasterConfig
}

var supported = map[string]string{
	"getHealth":       "getHealth",
	"getNetwork":      "getNetwork",
	"getVersionInfo":  "getVersionInfo",
	"getLatestLedger": "getLatestLedger",
}

// Entry/exit point from app.go
// RunVegeta runs a load test using Vegeta using the config settings through the RunEngine interface
// Sets up shared HTTP client, constructs per-endpoint blast configs, and fires off the blasts asynchronously
func RunVegeta(ctx context.Context, cfg RunEngine, out chan<- blasterMetrics.Sample) error {
	// Build shared HTTP client
	httpClient := NewHTTPClient(
		BlasterOptions{
			Timeout:   30 * time.Second,
			KeepAlive: true,
			// TODO: more HTTP client options? if needed, haven't looked into this yet
		})
	blasterBuilder := func() *vegeta.Attacker {
		return NewBlaster(httpClient)
	}

	// Construct endpoint blast configs
	// 3... 2... 1...
	var endpointBlasts []endpointBlast
	for _, endpointKey := range cfg.GetEndpoints() {
		method, ok := supported[endpointKey]
		if !ok {
			return fmt.Errorf("unsupported endpoint key: %s", endpointKey)
		}

		rps, numClients := cfg.GetEndpoint(endpointKey)
		if numClients <= 0 {
			numClients = 1
		}

		// Form request and Vegeta targeter from that
		request := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  method,
			"params":  map[string]any{}, // optional -- to be used when we do PR 573/data-dependent endpoints
		}
		body, _ := json.Marshal(request)
		targeter := NewJSONRPCTargeter(cfg.GetRPCUrl(), body)

		endpointBlasts = append(endpointBlasts, endpointBlast{
			EndpointBlastConfig: EndpointBlastConfig{
				EndpointKey: endpointKey,
				Method:      method,
				RPS:         rps,
				NumClients:  numClients,
				Targeter:    targeter,
			},
			BlasterConfig: BlasterConfig{
				Duration: cfg.GetDuration(),
				Ramp: Ramp{
					RampUp: cfg.GetRampUp(),
					Step:   time.Second,
					MaxRPS: rps,
				},
			},
		})
	}

	// Fire!
	var wg sync.WaitGroup
	errCh := make(chan error, len(endpointBlasts))
	for _, blast := range endpointBlasts {
		wg.Add(1)
		// if serialization through flags: wg2 goes here
		go func(eb endpointBlast) {
			defer wg.Done()
			if err := blastAtEndpoint(ctx, eb.EndpointBlastConfig, eb.BlasterConfig, blasterBuilder, out); err != nil {
				errCh <- fmt.Errorf("endpoint %s: %w", eb.EndpointKey, err)
			}
		}(blast)
		// end wg2
	}
	wg.Wait()
	close(errCh)

	// Collect errors
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
