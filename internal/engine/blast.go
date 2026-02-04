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

type BlasterConfig struct {
	Duration time.Duration
	Ramp     Ramp
}

// Run the blaster at a given endpoint
func blastAtEndpoint(
	ctx context.Context,
	endpointCfg EndpointBlastConfig,
	blastCfg BlasterConfig,
	newBlaster func() *vegeta.Attacker,
	out chan<- blasterMetrics.Sample,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Determine step duration for ramping
	step := blastCfg.Ramp.stepDuration()

	// Check remaining time
	sRemaining := blastCfg.Duration
	if sRemaining <= 0 {
		return
	}

	// Ramping up period
	if blastCfg.Ramp.RampUp > 0 {
		endDuration := min(blastCfg.Ramp.RampUp, sRemaining)
		for elapsed := time.Duration(0); elapsed < endDuration; {
			select {
			case <-ctx.Done():
				return
			default:
			}

			phaseStep := min(step, endDuration-elapsed)
			currentRPS := blastCfg.Ramp.rampRPS(elapsed)
			rate := vegeta.Rate{Freq: currentRPS, Per: time.Second}

			blaster := newBlaster()
			results := blaster.Attack(endpointCfg.Targeter, rate, phaseStep, endpointCfg.EndpointKey)

			flushResults(ctx, endpointCfg.EndpointKey, currentRPS, results, out)
			elapsed += phaseStep
		}
		sRemaining -= endDuration
	}

	// Steady state of full RPS period
	if sRemaining > 0 {
		rate := vegeta.Rate{Freq: blastCfg.Ramp.MaxRPS, Per: time.Second}
		blaster := newBlaster()
		results := blaster.Attack(endpointCfg.Targeter, rate, sRemaining, endpointCfg.EndpointKey)
		flushResults(ctx, endpointCfg.EndpointKey, blastCfg.Ramp.MaxRPS, results, out)
	}
}

// Reads results from a Vegeta results channel and forwards them to the output channel as a blasterMetrics.Sample
func flushResults(
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

func sleepCancel(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}
