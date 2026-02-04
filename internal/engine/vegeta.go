package engine

import (
	"net/http"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// Fraction of available ephemeral ports to use for connections.
// currently 0.60 = moderate-conservative, leaves 40% headroom for system processes.
const PortAllocationRatio = 0.60

// Ratio determining how many workers can share one connection
const WorkerMultiplier = 2.5

// Maximum time to wait for a single RPC response
// (for most endpoints -- this isn't accessible from outside stellar-rpc)
const RequestTimeout = 15 * time.Second

// SharedHTTPClient creates an HTTP client with connection sharing across workers based on available ephemeral ports
func SharedHTTPClient() (*http.Client, uint64) {
	totalPorts := EphemeralPortCount()
	maxConns := int(float64(totalPorts) * PortAllocationRatio)
	maxWorkers := uint64(float64(maxConns) * WorkerMultiplier)

	return &http.Client{
		Timeout: RequestTimeout,
		Transport: &http.Transport{
			MaxConnsPerHost: maxConns,
			IdleConnTimeout: RequestTimeout * 3, // keep connections warm for a few request cycles
		},
	}, maxWorkers
}

// NewBlasterWithClient creates a Vegeta attacker with a shared HTTP client and capped workers
func NewBlasterWithClient(client *http.Client, workers uint64) *vegeta.Attacker {
	return vegeta.NewAttacker(
		vegeta.Client(client),
		vegeta.MaxWorkers(workers),
		vegeta.KeepAlive(true),
	)
}
