package engine

import (
	"time"
)

// RunEngine provides the config getters needed to run a load test
type RunEngine interface {
	GetRPCUrl() string
	GetDuration() time.Duration
	GetRampUp() time.Duration
	GetEndpoints() []string
	GetEndpoint(key string) (rps int, numClients int)
}
