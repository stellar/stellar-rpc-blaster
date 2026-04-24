package parameters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stretchr/testify/require"
)

type jsonRPCRequest struct {
	ID     any    `json:"id"`
	Method string `json:"method"`
}

func newPreflightValidationClient(
	t *testing.T,
	healthResponse protocol.GetHealthResponse,
) endpointSeedValidationClient {
	t.Helper()

	getHealthHttpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		defer r.Body.Close()

		var req jsonRPCRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req)) // store r's body in req for assertion
		require.Equal(t, protocol.GetHealthMethodName, req.Method)

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  healthResponse,
		}))
	})
	server := httptest.NewServer(getHealthHttpHandler)
	client := rpcclient.NewClient(server.URL, server.Client())
	// close the server and client when the test finishes
	t.Cleanup(func() {
		server.Close()
		require.NoError(t, client.Close())
	})

	return client
}

func TestValidatorChecksSeedStartForSeededEndpoints(t *testing.T) {
	client := newPreflightValidationClient(t, protocol.GetHealthResponse{
		LatestLedger: 500,
		OldestLedger: 100,
	})
	params := &Parameters{Output: seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 456}}}

	err := params.ValidateConfiguredEndpoints(
		context.Background(),
		client,
		[]string{"getEvents"},
	)
	require.NoError(t, err, "parameters are in the retention window, but validation failed")
}

func TestValidatorSkipsStaticEndpoints(t *testing.T) {
	client := newPreflightValidationClient(t, protocol.GetHealthResponse{
		LatestLedger: 5000,
		OldestLedger: 1000,
	})
	params := &Parameters{Output: seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 456}}}

	err := params.ValidateConfiguredEndpoints(
		context.Background(),
		client,
		[]string{"getHealth", "getNetwork"},
	)
	require.NoError(t, err, "data is stale, but static endpoints should be skipped")
}

func TestValidatorRejectsStaleStart(t *testing.T) {
	client := newPreflightValidationClient(t, protocol.GetHealthResponse{
		LatestLedger: 400,
		OldestLedger: 300,
	})
	params := &Parameters{Output: seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 350}}}

	err := params.ValidateConfiguredEndpoints(
		context.Background(),
		client,
		[]string{"getEvents"},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "seed data start ledger 123 is outside the RPC retention window [300, 400]")
}

func TestValidatorRejectsMissingLatestLedger(t *testing.T) {
	client := newPreflightValidationClient(t, protocol.GetHealthResponse{
		OldestLedger: 100,
	})
	params := &Parameters{Output: seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 456}}}

	err := params.ValidateConfiguredEndpoints(
		context.Background(),
		client,
		[]string{"getLedgerEntries"},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "target RPC health check did not report a latest ledger")
}
