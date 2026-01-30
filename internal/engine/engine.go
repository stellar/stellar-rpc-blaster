package engine

import (
	"time"
)

// RunEngine provides the config getters needed to run a load test
type RunEngine interface {
	GetRPCUrl() string
	GetDuration() time.Duration
	GetRampUp() time.Duration
	GetEndpoints() map[string]EndpointEngine
}

// EndpointEngine provides per-endpoint config getters
type EndpointEngine interface {
	GetRPS() int
	GetNumClients() int
}
