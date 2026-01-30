package engine

import (
	"context"
	"sync"
	"time"

	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/metrics"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type EndpointBlastConfig struct {
	EndpointKey string // config key
	Method      string // JSON-RPC method
	RPS         int
	NumClients  int
	Targeter    vegeta.Targeter
}

type BlasterConfig struct {
	Duration time.Duration
	Ramp     Ramp
}

func runEndpointBlast(
	ctx context.Context,
	endpointCfg EndpointBlastConfig,
	blastCfg BlasterConfig,
	newBlaster func() *vegeta.Attacker,
	out chan<- blasterMetrics.Sample,
) error {
	var wg sync.WaitGroup
	wg.Add(endpointCfg.NumClients)

	for id := range endpointCfg.NumClients {
		blaster := newBlaster()

		go func() {
			defer wg.Done()
			runClient(ctx, endpointCfg, blastCfg, id, blaster, out)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func runClient(
	ctx context.Context,
	endpointCfg EndpointBlastConfig,
	blastCfg BlasterConfig,
	clientID int,
	blaster *vegeta.Attacker,
	out chan<- blasterMetrics.Sample,
) {
	step := blastCfg.Ramp.step()

	// Check remaining time
	sRemaining := blastCfg.Duration
	if sRemaining <= 0 {
		return
	}

	// Ramping up state
	if blastCfg.Ramp.RampUp > 0 {
		endDuration := min(blastCfg.Ramp.RampUp, sRemaining)
		for elapsed := time.Duration(0); elapsed < endDuration; {
			select {
			case <-ctx.Done():
				return
			default:
			}

			phaseStep := minDuration(step, endDuration-elapsed)
			currentRPS := blastCfg.Ramp.rampRPS(elapsed)
			if currentRPS > 0 {
				rate := vegeta.Rate{Freq: currentRPS, Per: time.Second}
				results := blaster.Attack(endpointCfg.Targeter, rate, phaseStep, endpointCfg.EndpointKey)
				flushResults(ctx, clientID, endpointCfg, results, out)
			} else {
				sleepCancel(ctx, phaseStep)
			}
			elapsed += phaseStep
		}
		sRemaining -= endDuration
	}

	// Steady state
	if sRemaining > 0 {
		rate := vegeta.Rate{Freq: blastCfg.Ramp.MaxRPS, Per: time.Second}
		results := blaster.Attack(endpointCfg.Targeter, rate, sRemaining, endpointCfg.EndpointKey)
		flushResults(ctx, clientID, endpointCfg, results, out)
	}
}

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
				ClientID: clientId,
				Endpoint: endpointCfg.EndpointKey,
				Method:   endpointCfg.Method,
				TS:       result.Timestamp,
				Latency:  result.Latency,
				Code:     result.Code,
				BytesIn:  result.BytesIn,
				BytesOut: result.BytesOut,
				Err:      result.Error,
				OK:       result.Error == "" && result.Code >= 200 && result.Code < 300,
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

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
