package parameters

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

var testHeadRange = protocol.LedgerSeqRange{FirstLedger: testOldest, LastLedger: testLatest}

// txParams builds a Parameters with a hash pool large enough that fresh-draw
// collisions don't pollute the repoll measurement.
func txParams(nHashes int) *Parameters {
	hashes := make([]string, nHashes)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i+1)
	}
	return &Parameters{
		Output: seed.SeedData{Version: seed.CurrentSeedVersion, TxHashes: hashes},
		Head:   HeadInfo{Oldest: testOldest, Latest: testLatest},
	}
}

func TestGetTransactionModel(t *testing.T) {
	const n = 20_000
	params := txParams(200_000)
	seedSet := make(map[string]bool, len(params.Output.TxHashes))
	for _, h := range params.Output.TxHashes {
		seedSet[h] = true
	}

	bodies, err := BuildEndpointParams("getTransaction", n, params, 0)
	require.NoError(t, err)
	require.Len(t, bodies, n)

	hexHash := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]bool{}
	repolls, notFound := 0, 0
	for _, body := range bodies {
		require.Len(t, body, 1, "only the hash key should be set: %v", body)
		hash := body["hash"].(string)
		require.Regexp(t, hexHash, hash)
		if seen[hash] {
			repolls++
		}
		seen[hash] = true
		if !seedSet[hash] {
			notFound++
		}
	}
	// seen-before = explicit repolls + never-land pool reuse (dead hashes are re-polled
	// by nature, as in the capture): PrTxRepoll + (1-PrTxRepoll)*PrTxNotFound ~= 0.65
	seenBefore := util.PrTxRepoll + (1-util.PrTxRepoll)*util.PrTxNotFound
	require.InDelta(t, seenBefore, float64(repolls)/n, 0.03, "repoll share")
	require.InDelta(t, util.PrTxNotFound, float64(notFound)/n, 0.02, "never-land share")
}

func TestGetTransactionsModel(t *testing.T) {
	const n = 20_000
	bodies, err := BuildEndpointParams("getTransactions", n, txParams(1), 0)
	require.NoError(t, err)

	near := 0
	for _, body := range bodies {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		var req protocol.GetTransactionsRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		require.NoError(t, req.IsValid(uint(util.MaxTxPageLimit), testHeadRange), "body: %s", raw)
		require.Empty(t, req.Format)
		require.EqualValues(t, util.MaxTxPageLimit, req.Pagination.Limit)
		if req.StartLedger >= testLatest-1000 {
			near++
		}
	}
	require.InDelta(t, util.PrTxsNearHead, float64(near)/n, 0.01, "near-head share")
}

func TestGetLedgersModel(t *testing.T) {
	const n = 20_000
	bodies, err := BuildEndpointParams("getLedgers", n, txParams(1), 0)
	require.NoError(t, err)

	near := 0
	limits := map[uint]int{}
	for _, body := range bodies {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		var req protocol.GetLedgersRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		require.NoError(t, req.Validate(uint(util.MaxLedgersPageLimit), testHeadRange), "body: %s", raw)
		require.Empty(t, req.Format)
		if req.Pagination != nil {
			limits[req.Pagination.Limit]++
		}
		if req.StartLedger >= testLatest-1000 {
			near++
		}
	}
	require.InDelta(t, util.PrLedgersNearHead, float64(near)/n, 0.015, "near-head share")
	for limit, want := range map[uint]float64{1: 0.4, 5: 0.28, 20: 0.07} {
		require.InDelta(t, want, float64(limits[limit])/n, 0.015, "limit %d share", limit)
	}
	require.InDelta(t, 0.25, float64(n-limits[1]-limits[5]-limits[20])/n, 0.015, "omitted-pagination share")
}

func TestLedgerWindowLimitOverride(t *testing.T) {
	for _, endpoint := range []string{"getTransactions", "getLedgers"} {
		bodies, err := BuildEndpointParams(endpoint, 200, txParams(1), 77)
		require.NoError(t, err)
		for _, body := range bodies {
			require.EqualValues(t, map[string]any{"limit": uint32(77)}, body["pagination"], endpoint)
		}
	}
}
