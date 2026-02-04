package blasterMetrics

import (
	"time"
)

type Sample struct {
	Endpoint   string    // config key / JSON-RPC method (e.g. "getHealth")
	CurrentRPS int       // RPS at the time of the sample
	Timestamp  time.Time // timestamp of the sample
	Latency    time.Duration
	Code       uint16
	BytesIn    uint64
	BytesOut   uint64
	Err        string // raw error from vegeta (if any)
	OK         bool
}
