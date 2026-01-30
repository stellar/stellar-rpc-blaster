package engine

import (
	"math"
	"time"
)

type Ramp struct {
	RampUp time.Duration // seconds for RPS to reach max RPS
	Step   time.Duration
	MaxRPS int // maximum requests per second
}

func (r Ramp) step() time.Duration {
	if r.Step <= 0 {
		return time.Second
	}
	return r.Step
}

// rampRPS calculates the RPS at a given elapsed time during the ramp-up period.
func (r Ramp) rampRPS(elapsed time.Duration) int {
	if r.RampUp <= 0 || elapsed >= r.RampUp {
		return r.MaxRPS
	}

	scale := float64(elapsed) / float64(r.RampUp)
	return int(math.Round(scale * float64(r.MaxRPS)))
}
