package types

import "time"

type LoadTestSettings interface {
	GetRpcUrl() string
	GetDuration() time.Duration
	GetRampUp() time.Duration
	GetEndpoints() []string
	GetEndpointRPS(key string) int
	GetOutputPath() string
}
