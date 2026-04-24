package parameters

import (
	"context"
	"encoding/json"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
	"github.com/stretchr/testify/require"
)

func TestValidatorChecksSeedStartForSeededEndpoints(t *testing.T) {
	client := util.NewMockRPCClient(t, func(method string, _ json.RawMessage) any {
		return protocol.GetHealthResponse{
			LatestLedger: 500,
			OldestLedger: 100,
		}
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
	client := util.NewMockRPCClient(t, func(method string, _ json.RawMessage) any {
		return protocol.GetHealthResponse{
			LatestLedger: 500,
			OldestLedger: 100,
		}
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
	client := util.NewMockRPCClient(t, func(method string, _ json.RawMessage) any {
		return protocol.GetHealthResponse{
			LatestLedger: 500,
			OldestLedger: 100,
		}
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
	client := util.NewMockRPCClient(t, func(method string, _ json.RawMessage) any {
		return protocol.GetHealthResponse{
			OldestLedger: 100,
		}
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
