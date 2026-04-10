package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
)

func newTestPacer(startRPS *int, maxRPS int, rampUp, duration, stepInterval time.Duration) SteppedPacer {
	return NewSteppedPacer("test", config.Config{
		RampUp:       rampUp,
		Duration:     duration,
		StepInterval: stepInterval,
		Endpoints: map[string]config.EndpointConfig{
			"test": {
				StartRPS: startRPS,
				RPS:      maxRPS,
			},
		},
	})
}


func TestRateStepsUpThenConstantAfter(t *testing.T) {
	var (
		// 30s ramp with 10s steps -> 3 steps, stepSize = (40-10)/3 = 10
		startRps, maxRps    = 10, 40
		rampUpDur, totalDur = 30 * time.Second, 60 * time.Second
		stepInterval        = 10 * time.Second
	)
	p := newTestPacer(&startRps, maxRps, rampUpDur, totalDur, stepInterval)

	require.Equal(t, float64(startRps), p.Rate(0*time.Second), "Rate(0s) should be startRPS")
	require.Equal(t, 20.0, p.Rate(10*time.Second), "Rate(10s)")
	require.Equal(t, 30.0, p.Rate(20*time.Second), "Rate(20s)")
	require.Equal(t, float64(maxRps), p.Rate(rampUpDur), "Rate at ramp end")
	require.Equal(t, float64(maxRps), p.Rate(rampUpDur+10*time.Second), "Rate after ramp should stay constant")
}

func TestRateFromZeroStartRPS(t *testing.T) {
	var (
		// 20s ramp with 10s steps -> 2 steps, stepSize = (20-0)/2 = 10
		startRps, maxRps    = 0, 20
		rampUpDur, totalDur = 20 * time.Second, 60 * time.Second
		stepInterval        = 10 * time.Second
	)
	p := newTestPacer(&startRps, maxRps, rampUpDur, totalDur, stepInterval)

	require.Equal(t, 0.0, p.Rate(0), "Rate(0s) should be 0 when explicitly set")
	require.Equal(t, float64(maxRps), p.Rate(rampUpDur), "Rate at ramp end")
	require.Equal(t, float64(maxRps), p.Rate(rampUpDur+10*time.Second), "Rate after ramp should stay constant")
}

func TestHitsMonotonicallyIncreasing(t *testing.T) {
	startRPS := 5
	p := newTestPacer(&startRPS, 50, 30*time.Second, 60*time.Second, 5*time.Second)

	prev := 0.0
	for s := 0; s <= 60; s++ {
		elapsed := time.Duration(s) * time.Second
		hits := p.Hits(elapsed)
		require.GreaterOrEqual(t, hits, prev, "Hits(%ds) should be >= Hits(%ds)", s, s-1)
		prev = hits
	}
}

func TestHitsContinuousAtRampBoundary(t *testing.T) {
	p := newTestPacer(nil, 20, 30*time.Second, 60*time.Second, 10*time.Second)

	justBefore := p.Hits(30*time.Second - time.Millisecond)
	atBoundary := p.Hits(30 * time.Second)
	justAfter := p.Hits(30*time.Second + time.Millisecond)

	require.InDelta(t, justBefore, atBoundary, 0.1, "discontinuity before ramp boundary")
	require.InDelta(t, atBoundary, justAfter, 0.1, "discontinuity after ramp boundary")
}

func TestHitsAfterRampGrowsAtMaxRate(t *testing.T) {
	p := newTestPacer(nil, 20, 10*time.Second, 60*time.Second, 5*time.Second)

	hitsAtRamp := p.Hits(10 * time.Second)
	hitsAt20 := p.Hits(20 * time.Second)

	// After ramp, should grow at exactly maxRPS (20) per second
	require.InDelta(t, 200.0, hitsAt20-hitsAtRamp, 0.01, "post-ramp growth should be maxRPS * elapsed")
}

func TestPaceCatchesUpWhenBehind(t *testing.T) {
	p := newTestPacer(nil, 20, 10*time.Second, 60*time.Second, 5*time.Second)

	// At 15s post-ramp, expected hits is substantial; 0 actual hits means we're behind
	wait, stop := p.Pace(15*time.Second, 0)
	require.False(t, stop, "Pace should not signal stop")
	require.Equal(t, time.Duration(0), wait, "Pace should return wait=0 when behind")
}

func TestPartialLastStep(t *testing.T) {
	// 12s ramp with 5s steps: steps 0-5s, 5-10s are full; 10-12s is partial
	p := newTestPacer(nil, 30, 12*time.Second, 60*time.Second, 5*time.Second)

	// Rate in the partial step (10-12s) should still be valid and > rate at step 1
	require.Greater(t, p.Rate(11*time.Second), p.Rate(5*time.Second), "partial step rate should exceed step1 rate")

	// Hits should be continuous through the partial step
	require.Greater(t, p.Hits(12*time.Second), p.Hits(11*time.Second), "Hits should increase through partial step")

	// At ramp end, should switch to maxRPS
	require.Equal(t, 30.0, p.Rate(12*time.Second), "Rate at ramp end")
}
