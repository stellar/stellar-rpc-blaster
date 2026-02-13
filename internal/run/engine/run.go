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

	// Pre-load seed parameters for data-dependent endpoints, deduplicating by path
	paramCache := map[string]*parameters.Parameters{}
	for _, endpointKey := range cfg.GetEndpoints() {
		ep := cfg.Endpoints[endpointKey]
		if ep.DataPath == "" || !parameters.EndpointNeedsData(endpointKey) {
			continue
		}
		if _, ok := paramCache[ep.DataPath]; !ok {
			p, err := parameters.GetParameters(ep.DataPath)
			if err != nil {
				return errors.Wrapf(err, "loading seed data for endpoint %s", endpointKey)
			}
			paramCache[ep.DataPath] = p
		}
	}

	// Construct endpoint blast configs
	// 3... 2... 1...
	var endpointBlasts []endpointBlast
	for _, endpointKey := range cfg.GetEndpoints() {
		rps := cfg.GetEndpointRPS(endpointKey)
		ep := cfg.Endpoints[endpointKey]

		var targeter vegeta.Targeter
		if params, ok := paramCache[ep.DataPath]; ok && parameters.EndpointNeedsData(endpointKey) {
			// Data-dependent endpoint: build variant request bodies and rotate through them
			paramMaps, err := parameters.BuildEndpointParams(endpointKey, params)
			if err != nil {
				return errors.Wrapf(err, "couldn't build params for endpoint %s", endpointKey)
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
					return errors.Wrapf(err, "couldn't marshal request for endpoint %s", endpointKey)
				}
			}
			targeter = NewRotatingJSONRPCTargeter(cfg.RpcUrl, bodies)
			logger.Infof("Endpoint %s: rotating through %d parameterized request bodies", endpointKey, len(bodies))
		} else {
			// Static endpoint: single body with empty params
			request := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  endpointKey,
				"params":  map[string]any{},
			}
			body, err := json.Marshal(request)
			if err != nil {
				return errors.Wrapf(err, "couldn't marshal JSON request for static endpoint %s", endpointKey)
			}
			targeter = NewJSONRPCTargeter(cfg.RpcUrl, body)
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
