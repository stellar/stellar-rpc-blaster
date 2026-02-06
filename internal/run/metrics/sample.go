package blasterMetrics

import (
	"time"
)

// Represents a single blaster metrics sample, bridging a vegeta.Result to the aggregator
type Sample struct {
	Endpoint   string  // config key / JSON-RPC method (e.g. "getHealth")
	CurrentRPS float64 // expected cumulative RPS at the time of the sample
	Latency    time.Duration
	Code       uint16
	Err        string // raw error from vegeta (if any)
	OK         bool
}
