package engine

import (
	"context"

	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/metrics"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type EndpointBlastConfig struct {
	EndpointKey string // config key / JSON-RPC method
	RPS         int
	Targeter    vegeta.Targeter
}

// Run the blaster at a given endpoint
func blastAtEndpoint(
	ctx context.Context,
	endpointCfg EndpointBlastConfig,
	blastPacer RampToConstantPacer,
	newBlaster func() *vegeta.Attacker,
	out chan<- blasterMetrics.Sample,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if blastPacer.TotalDuration <= 0 {
		return
	}

	blaster := newBlaster()
	results := blaster.Attack(endpointCfg.Targeter, blastPacer, blastPacer.TotalDuration, endpointCfg.EndpointKey)
	flushBlastResults(ctx, endpointCfg.EndpointKey, blastPacer.MaxRPS, results, out)
}

// Reads results from a Vegeta results channel and forwards them to the output channel as a blasterMetrics.Sample
func flushBlastResults(
	ctx context.Context,
	endpointKey string,
	targetRPS int,
	results <-chan *vegeta.Result,
	out chan<- blasterMetrics.Sample,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case result, ok := <-results:
			if !ok {
				return
			}
			out <- blasterMetrics.Sample{
				Endpoint:   endpointKey,
				CurrentRPS: targetRPS,
				Timestamp:  result.Timestamp,
				Latency:    result.Latency,
				Code:       result.Code,
				BytesIn:    result.BytesIn,
				BytesOut:   result.BytesOut,
				Err:        result.Error,
				OK:         result.Error == "" && result.Code >= 200 && result.Code < 300,
			}
		}
	}
}
