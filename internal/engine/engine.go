package engine

import (
	"time"
)

// RunEngine provides the configuration needed to run a load test
type RunEngine interface {
	GetRPCUrl() string
	GetDuration() time.Duration
	GetRampUp() time.Duration
	GetEndpoints() map[string]EndpointEngine
}

// EndpointEngine provides per-endpoint configuration
type EndpointEngine interface {
	GetRPS() int
	GetNumClients() int
}
