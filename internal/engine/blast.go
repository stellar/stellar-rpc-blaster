package engine

import (
	"context"
	"time"

	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/metrics"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type EndpointBlastConfig struct {
	EndpointKey string // config key / JSON-RPC method
	RPS         int
	Targeter    vegeta.Targeter
}

// Ramps linearly from StartRPS to MaxRPS over RampDuration, then holds constant at MaxRPS
// Satisfies vegeta.Pacer interface
type RampToConstantPacer struct {
	StartRPS      int
	MaxRPS        int
	RampDuration  time.Duration
	TotalDuration time.Duration
}

// implements vegeta.Pacer, returns wait time until next hit and whether to stop
func (p RampToConstantPacer) Pace(elapsed time.Duration, hits uint64) (time.Duration, bool) {
	expectedHits := p.hits(elapsed)
	if hits < uint64(expectedHits) {
		// Running behind, fire blaster now
		return 0, false
	}

	// Calculate wait time until next hit
	rate := p.Rate(elapsed)
	if rate <= 0 {
		return 0, true // Stop if rate is invalid
	}

	interval := 1e9 / rate // nanoseconds per hit
	delta := float64(hits+1) - expectedHits
	wait := time.Duration(interval * delta)
	return wait, false
}

// Rate implements vegeta.Pacer - returns instantaneous hits per second
func (p RampToConstantPacer) Rate(elapsed time.Duration) float64 {
	if elapsed >= p.RampDuration {
		return float64(p.MaxRPS)
	}
	// Linear interpolation: StartRPS + (MaxRPS - StartRPS) * (elapsed / RampDuration)
	progress := float64(elapsed) / float64(p.RampDuration)
	return float64(p.StartRPS) + progress*float64(p.MaxRPS-p.StartRPS)
}

// hits returns expected cumulative hits at elapsed time
// During ramp: integral of rate(t) = startRate + slope*t -> startRate*t + (slope*t²)/2
// After ramp: ramp hits + MaxRPS * (t - RampDuration)
func (p RampToConstantPacer) hits(elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}

	startRate := float64(p.StartRPS)
	maxRate := float64(p.MaxRPS)

	// Past ramp-up: all ramp hits + constant rate hits
	if elapsed >= p.RampDuration {
		// Hits during ramp is area of trapezoid since startRate >= 0
		rampHits := (startRate + maxRate) / 2 * p.RampDuration.Seconds()
		// Hits after ramp at constant MaxRPS
		afterRampSecs := (elapsed - p.RampDuration).Seconds()
		return rampHits + maxRate*afterRampSecs
	}

	// Hits during ramp-up
	t := elapsed.Seconds()
	slope := (maxRate - startRate) / p.RampDuration.Seconds()
	return startRate*t + (slope*t*t)/2
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
