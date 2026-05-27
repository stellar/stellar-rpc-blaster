package engine

import (
	"net/http"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// NewBlasterWithClient creates a Vegeta attacker with a shared HTTP client and capped workers
func NewBlasterWithClient(client *http.Client, workers uint64) *vegeta.Attacker {
	return vegeta.NewAttacker(
		vegeta.Client(client),
		vegeta.MaxWorkers(workers),
		vegeta.KeepAlive(true),
	)
}
