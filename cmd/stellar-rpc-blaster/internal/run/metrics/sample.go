package blasterMetrics

import (
	"time"

	"github.com/creachadair/jrpc2"
)

// Represents a single blaster metrics sample, bridging a vegeta.Result to the aggregator
type Sample struct {
	Endpoint   string  // config key / JSON-RPC method (e.g. "getHealth")
	Archetype  string  // traffic-model archetype label; also tracked as a sub-stream when set
	CurrentRPS float64 // instantaneous target RPS at the time of the sample
	Latency    time.Duration
	Code       uint16
	Err        string       // raw error from vegeta (if any)
	RPCErr     *jrpc2.Error // JSON-RPC error from response body (e.g. "internal error (-32603)")
	OK         bool
}
