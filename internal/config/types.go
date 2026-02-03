package types

import "time"

type LoadTestSettings interface {
	GetRpcUrl() string
	GetDuration() time.Duration
	GetRampUp() time.Duration
	GetEndpoints() []string
	GetEndpoint(key string) (rps int, numClients int)
	GetTotalNumClients() int
}
