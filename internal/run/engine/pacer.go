package engine

import (
	"time"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
)

// Steps from StartRPS to MaxRPS over RampDuration in fixed 5-second steps, then holds constant at MaxRPS.
// The last step may be shorter than 5s if RampDuration isn't a multiple of stepInterval.
// Satisfies vegeta.Pacer interface.
type SteppedPacer struct {
	StartRPS      int
	MaxRPS        int
	StepSize      float64 // RPS increase per step
	StepInterval  time.Duration
	RampDuration  time.Duration
	TotalDuration time.Duration
}

func NewSteppedPacer(endpointKey string, cfg config.Config) SteppedPacer {
	startRPS, maxRPS := cfg.GetEndpointStartRPS(endpointKey), cfg.GetEndpointTargetRPS(endpointKey)
	rampDuration := cfg.RampUp
	steps := max(float64(rampDuration)/float64(cfg.StepInterval), 1)

	// When startRPS is omitted (-1), bump it to the first step so we don't waste a step at 0 RPS.
	// When startRPS is explicitly set (including 0), honor it.
	if startRPS < 0 {
		stepSize := float64(maxRPS) / (steps + 1)
		startRPS = int(stepSize)
	}
	stepSize := float64(maxRPS-startRPS) / steps

	return SteppedPacer{
		StartRPS:      startRPS,
		MaxRPS:        maxRPS,
		StepSize:      stepSize,
		StepInterval:  cfg.StepInterval,
		RampDuration:  rampDuration,
		TotalDuration: cfg.Duration,
	}
}

// Rate returns the current step's RPS at the given elapsed time.
func (p SteppedPacer) Rate(elapsed time.Duration) float64 {
	if elapsed >= p.RampDuration {
		return float64(p.MaxRPS)
	}
	step := int(elapsed / p.StepInterval)
	return float64(p.StartRPS) + float64(step)*p.StepSize
}

// Hits returns expected cumulative hits at elapsed time.
// Each step is a constant-rate rectangle, with the last ramp step potentially shorter than stepInterval.
func (p SteppedPacer) Hits(elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}

	// Cap at ramp boundary, then add constant-rate hits after
	rampElapsed := min(elapsed, p.RampDuration)

	// Count requests from completed full steps (arithmetic series, 0-indexed)
	completedSteps := int(rampElapsed / p.StepInterval)
	stepSecs := p.StepInterval.Seconds()
	hits := stepSecs * (float64(completedSteps)*float64(p.StartRPS) + p.StepSize*float64(completedSteps)*float64(completedSteps-1)/2)
	// Add partial hits from current step
	remaining := (rampElapsed - time.Duration(completedSteps)*p.StepInterval).Seconds()
	currentRate := float64(p.StartRPS) + float64(completedSteps)*p.StepSize
	hits += currentRate * remaining

	// Add remaining hits from post-ramp constant rate
	remaining = max(0, (elapsed - p.RampDuration).Seconds())
	hits += float64(p.MaxRPS) * remaining

	return hits
}

// Pace implements vegeta.Pacer — returns wait time until next hit and whether to stop.
func (p SteppedPacer) Pace(elapsed time.Duration, hits uint64) (time.Duration, bool) {
	expectedHits := p.Hits(elapsed)
	if hits < uint64(expectedHits) {
		// Running behind, send now
		return 0, false
	}

	rate := p.Rate(elapsed)
	interval := float64(time.Second) / rate
	delta := float64(hits+1) - expectedHits
	wait := time.Duration(interval * delta)
	return wait, false
}
