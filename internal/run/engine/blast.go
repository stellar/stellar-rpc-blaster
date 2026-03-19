package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/creachadair/jrpc2"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/run/metrics"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type EndpointBlastConfig struct {
	EndpointKey string // config key / JSON-RPC method
	RPS         int
	Targeter    vegeta.Targeter
}

type JrpcErrEnvelope struct {
	Error *jrpc2.Error `json:"error,omitempty"`
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

	start := time.Now()
	blaster := newBlaster()
	results := blaster.Attack(endpointCfg.Targeter, blastPacer, blastPacer.TotalDuration, endpointCfg.EndpointKey)
	flushBlastResults(ctx, endpointCfg.EndpointKey, blastPacer, start, results, out)
}

// Reads results from a Vegeta results channel and forwards them to the output channel as a blasterMetrics.Sample
func flushBlastResults(
	ctx context.Context,
	endpointKey string,
	pacer RampToConstantPacer,
	start time.Time,
	results <-chan *vegeta.Result,
	out chan<- blasterMetrics.Sample,
) {
	for result := range results {
		elapsed := time.Since(start)
		expectedRPS := pacer.Hits(elapsed) / elapsed.Seconds()

		ok := result.Error == "" && result.Code >= 200 && result.Code < 300
		var rpcErr *jrpc2.Error
		if ok && len(result.Body) > 0 {
			var envelope JrpcErrEnvelope
			if err := json.Unmarshal(result.Body, &envelope); err == nil && envelope.Error != nil {
				rpcErr = envelope.Error
				ok = false
			}
		}

		select {
		case out <- blasterMetrics.Sample{
			Endpoint:   endpointKey,
			CurrentRPS: expectedRPS,
			Latency:    result.Latency,
			Code:       result.Code,
			Err:        result.Error,
			RPCErr:     rpcErr,
			OK:         ok,
		}:
		case <-ctx.Done():
			return
		}
	}
}
