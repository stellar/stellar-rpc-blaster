package engine

import (
	"context"
	"sync"
	"time"

	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/metrics"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type EndpointBlastConfig struct {
	EndpointKey string // config key / JSON-RPC method
	RPS         int
	NumClients  int
	Targeter    vegeta.Targeter
}

type BlasterConfig struct {
	Duration time.Duration
	Ramp     Ramp
}

// blastAtEndpoint runs the load test for a specific endpoint with multiple concurrent clients
func blastAtEndpoint(
	ctx context.Context,
	endpointCfg EndpointBlastConfig,
	blastCfg BlasterConfig,
	newBlaster func() *vegeta.Attacker,
	out chan<- blasterMetrics.Sample,
) error {
	var wg sync.WaitGroup
	var blasters []*vegeta.Attacker // track to stop on cancellation

	for id := range endpointCfg.NumClients {
		blaster := newBlaster()
		blasters = append(blasters, blaster)

		// Launch async client goroutine
		wg.Go(func() {
			runEndpointBlaster(ctx, endpointCfg, blastCfg, id, newBlaster, out)
		})
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		// Stop all attackers to cancel in-flight requests
		for _, b := range blasters {
			b.Stop()
		}
		// Wait for goroutines to finish draining
		<-done
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Single-client function to run the blast at one given endpoint
func runEndpointBlaster(
	ctx context.Context,
	endpointCfg EndpointBlastConfig,
	blastCfg BlasterConfig,
	clientID int,
	newBlaster func() *vegeta.Attacker,
	out chan<- blasterMetrics.Sample,
) {
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
			if currentRPS > 0 {
				rate := vegeta.Rate{Freq: currentRPS, Per: time.Second}
				blaster := newBlaster()
				results := blaster.Attack(endpointCfg.Targeter, rate, phaseStep, endpointCfg.EndpointKey)
				flushResults(ctx, clientID, endpointCfg, results, out)
			} else {
				// defensive, but force sleep/cancellation if RPS is 0
				sleepCancel(ctx, phaseStep)
			}
			elapsed += phaseStep
		}
		sRemaining -= endDuration
	}

	// Steady state of full RPS period
	if sRemaining > 0 {
		rate := vegeta.Rate{Freq: blastCfg.Ramp.MaxRPS, Per: time.Second}
		blaster := newBlaster()
		results := blaster.Attack(endpointCfg.Targeter, rate, sRemaining, endpointCfg.EndpointKey)
		flushResults(ctx, clientID, endpointCfg, results, out)
	}
}

// Reads results from a Vegeta results channel and forwards them to the output channel as a blasterMetrics.Sample
func flushResults(
	ctx context.Context,
	clientId int,
	endpointCfg EndpointBlastConfig,
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
				ClientId:   clientId,
				Endpoint:   endpointCfg.EndpointKey,
				CurrentRPS: endpointCfg.RPS,
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
