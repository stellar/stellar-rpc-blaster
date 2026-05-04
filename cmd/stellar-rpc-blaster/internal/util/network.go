package util

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
)

// SharedHTTPClient creates an HTTP client with connection sharing across workers based on available ephemeral ports
func SharedHTTPClient() *http.Client {
	totalPorts := PortCount
	maxConns := int(float64(totalPorts) * PortAllocationRatio)

	return &http.Client{
		Timeout: RequestTimeout,
		Transport: &http.Transport{
			MaxConnsPerHost:     maxConns,
			MaxIdleConnsPerHost: maxConns,
			MaxIdleConns:        0,                  // unlimited, MaxIdleConnsPerHost limits per-host idle connections
			IdleConnTimeout:     RequestTimeout * 3, // keep connections warm for a few request cycles
		},
	}
}

// NewMockRPCClient creates an httptest server that serves RPC responses for one method
// Used in testing across the repo
func NewMockRPCClient(t *testing.T, respond func(method string, params json.RawMessage) any) *rpcclient.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&env))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": env.ID, "result": respond(env.Method, env.Params),
		}))
	}))
	c := rpcclient.NewClient(srv.URL, srv.Client())
	t.Cleanup(func() { srv.Close(); _ = c.Close() })
	return c
}
