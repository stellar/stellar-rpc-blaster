package blasterMetrics

import (
	"time"
)

type Sample struct {
	ClientID  int       // ID of the client that generated this sample
	Endpoint  string    // config key (e.g. "getHealth", "getNetwork", etc.)
	Method    string    // JSON-RPC method: "getHealth", etc. (often same as Endpoint)
	TS        time.Time // timestamp of the sample
	Latency   time.Duration
	Code      uint16
	BytesIn   uint64
	BytesOut  uint64
	Err       string // raw error from vegeta (if any)
	ErrorType string // normalized bucket (filled by errors classifier)
	OK        bool
}
