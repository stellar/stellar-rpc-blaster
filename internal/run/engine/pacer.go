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

func NewSteppedPacer(startRPS, maxRPS int, cfg config.Config) SteppedPacer {
	rampDuration := cfg.RampUp
	steps := float64(rampDuration) / float64(cfg.StepInterval)
	if steps < 1 {
		steps = 1
	}
	stepSize := float64(maxRPS-startRPS) / (steps + 1)

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
	step := int(elapsed/p.StepInterval) + 1
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

	// Sum completed full steps + partial current step
	completedSteps := int(rampElapsed / p.StepInterval)
	var hits float64
	for i := range completedSteps {
		stepEnd := min(time.Duration(i+1)*p.StepInterval, p.RampDuration)
		stepDur := (stepEnd - time.Duration(i)*p.StepInterval).Seconds()
		rate := float64(p.StartRPS) + float64(i+1)*p.StepSize
		hits += rate * stepDur
	}
	remaining := (rampElapsed - time.Duration(completedSteps)*p.StepInterval).Seconds()
	currentRate := float64(p.StartRPS) + float64(completedSteps+1)*p.StepSize
	hits += currentRate * remaining

	// Constant rate after ramp
	if elapsed > p.RampDuration {
		hits += float64(p.MaxRPS) * (elapsed - p.RampDuration).Seconds()
	}
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
