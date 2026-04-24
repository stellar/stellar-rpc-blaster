package seed

import (
	"encoding/json"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
	"github.com/stretchr/testify/require"
)

// captureTransactions wires NewMockRPCClient for getTransactions: assert the method,
// decode into GetTransactionsRequest, record, delegate the response. The returned
// accessor returns the current slice each time it's called — read it after the
// seeder has finished running. Shared with ledger_keys_test.go.
func captureTransactions(t *testing.T, respond func() protocol.GetTransactionsResponse) (*rpcclient.Client, func() []protocol.GetTransactionsRequest) {
	var calls []protocol.GetTransactionsRequest
	client := util.NewMockRPCClient(t, func(method string, params json.RawMessage) any {
		require.Equal(t, protocol.GetTransactionsMethodName, method)
		var req protocol.GetTransactionsRequest
		require.NoError(t, json.Unmarshal(params, &req))
		calls = append(calls, req)
		return respond()
	})
	return client, func() []protocol.GetTransactionsRequest { return calls }
}

func stubTx(ledger uint32, hash string) protocol.TransactionInfo {
	return protocol.TransactionInfo{
		TransactionDetails: protocol.TransactionDetails{
			Status:          "SUCCESS",
			TransactionHash: hash,
			Ledger:          ledger,
		},
	}
}

// TestTxHashSeeder_CapturesHashesAndUsesCursor verifies the two-call happy path:
// involving an initial range-driven getTxs call and then a cursor driven one
func TestTxHashSeeder_CapturesHashesAndUsesCursor(t *testing.T) {
	page := 0
	client, calls := captureTransactions(t, func() protocol.GetTransactionsResponse {
		page++
		if page == 1 {
			return protocol.GetTransactionsResponse{
				Transactions: []protocol.TransactionInfo{
					stubTx(100, "hash-A"),
					stubTx(100, "hash-B"),
					stubTx(100, "hash-C"),
				},
				Cursor: "toid-1",
			}
		}
		return protocol.GetTransactionsResponse{} // empty → loop breaks
	})
	seeder := NewTxHashSeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 100, Last: 100}))

	got := calls()
	require.Len(t, got, 2, "one initial call plus one cursor-driven follow-up then break on empty")
	require.EqualValues(t, 100, got[0].StartLedger)
	require.Equal(t, "", got[0].Pagination.Cursor)
	require.EqualValues(t, 0, got[1].StartLedger, "cursor-based call zeroes startLedger per MakeGetTransactionsRequest")
	require.Equal(t, "toid-1", got[1].Pagination.Cursor)

	require.Equal(t, []string{"hash-A", "hash-B", "hash-C"}, seeder.data)
}

// TestTxHashSeeder_StopsAtRangeUpperBound verifies out of range txs aren't captured,
// even when the response contains in-range txs before it.
func TestTxHashSeeder_StopsAtRangeUpperBound(t *testing.T) {
	client, calls := captureTransactions(t, func() protocol.GetTransactionsResponse {
		return protocol.GetTransactionsResponse{
			Transactions: []protocol.TransactionInfo{
				stubTx(100, "in-range-1"),
				stubTx(105, "in-range-2"),
				stubTx(106, "OUT-OF-RANGE"), // r.Last == 105
				stubTx(107, "also-out"),
			},
			Cursor: "toid-1",
		}
	})
	seeder := NewTxHashSeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 100, Last: 105}))

	require.Len(t, calls(), 1, "seeder should return the moment it sees tx.Ledger > r.Last")
	require.Equal(t, []string{"in-range-1", "in-range-2"}, seeder.data,
		"hashes at or before r.Last are captured; anything beyond is dropped")
}
