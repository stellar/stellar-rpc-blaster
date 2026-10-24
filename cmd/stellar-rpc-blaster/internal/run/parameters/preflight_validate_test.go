package parameters

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

// seededParams returns Parameters whose seed fields are all populated, so the
// per-endpoint seed-field check passes and tests exercise the retention logic.
func seededParams(first, last uint32) *Parameters {
	return &Parameters{Output: seed.SeedData{
		LedgerRange: seed.Range{First: first, Last: last},
		TxHashes:    []string{"h"},
		LedgerKeys:  []string{"k"},
		ContractEventData: seed.ContractEvents{
			ContractIds: map[string]*seed.TopicData{"C": {Count: 1}},
		},
	}}
}

func TestValidatorChecksSeedStartForSeededEndpoints(t *testing.T) {
	client := util.NewMockRPCClient(t, func(method string, _ json.RawMessage) any {
		return protocol.GetHealthResponse{
			LatestLedger: 500,
			OldestLedger: 100,
		}
	})
	params := seededParams(123, 456)

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
	params := seededParams(123, 456)

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
			LatestLedger: 400,
			OldestLedger: 300,
		}
	})
	params := seededParams(123, 350)

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
	params := seededParams(123, 456)

	err := params.ValidateConfiguredEndpoints(
		context.Background(),
		client,
		[]string{"getLedgerEntries"},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "target RPC health check did not report a latest ledger")
}

func TestValidatorNarrowsToQueryWindow(t *testing.T) {
	client := util.NewMockRPCClient(t, func(method string, _ json.RawMessage) any {
		return protocol.GetHealthResponse{
			LatestLedger: 50_000,
			OldestLedger: 100,
		}
	})
	cases := map[string]struct {
		window        []uint32
		floor, latest uint32
		err           string
	}{
		"unset: [oldest+1000, latest]": {nil, 1100, 50_000, ""},
		"START only":                   {[]uint32{20_000}, 20_000, 50_000, ""},
		"START,END":                    {[]uint32{20_000, 30_000}, 20_000, 30_000, ""},
		"clamped at both ends":         {[]uint32{500, 60_000}, 1100, 50_000, ""},
		"entirely below the floor":     {[]uint32{200, 900}, 0, 0, "query-window [200, 900] is outside the target's queryable ledgers [1100, 50000]"},
		"START past latest":            {[]uint32{60_000}, 0, 0, "query-window start 60000 is after its end 50000"},
		"START after END":              {[]uint32{30_000, 20_000}, 0, 0, "query-window start 30000 is after its end 20000"},
		"too many values":              {[]uint32{1, 2, 3}, 0, 0, "query-window must have at most 2 values, got 3"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			params := seededParams(200, 400)
			params.QueryWindow = c.window
			err := params.ValidateConfiguredEndpoints(context.Background(), client, []string{"getEvents"})
			if c.err != "" {
				require.ErrorContains(t, err, c.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, [2]uint32{c.floor, c.latest}, [2]uint32{params.Head.Floor(), params.Head.Latest})
		})
	}
}
