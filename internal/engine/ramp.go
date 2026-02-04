package engine

import (
	"math"
	"time"
)

// Vegeta doesn't have any real bre-built ramping support, so we implement
// a discrete ramping mechanism here
type Ramp struct {
	RampUp time.Duration // seconds for RPS to reach max RPS
	Step   time.Duration // discrete time tick before ratching next RPS
	MaxRPS int           // maximum requests per second (target given in config toml)
}

func (r Ramp) stepDuration() time.Duration {
	if r.Step <= 0 {
		return time.Second
	}
	return r.Step
}

// rampRPS calculates the RPS at a given elapsed time during the ramp-up period.
// Returns at least 1 RPS to avoid Vegeta interpreting 0 as "infinite rate".
func (r Ramp) rampRPS(elapsed time.Duration) int {
	if r.RampUp <= 0 || elapsed >= r.RampUp {
		return r.MaxRPS
	}

	scale := float64(elapsed) / float64(r.RampUp)
	return max(int(math.Round(scale*float64(r.MaxRPS))), 1)
}
