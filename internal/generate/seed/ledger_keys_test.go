package seed

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/collections/set"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
	"github.com/stretchr/testify/require"
)

func newTestLedgerKeySeeder(client *rpcclient.Client) *LedgerKeySeeder {
	return &LedgerKeySeeder{
		rpcClient: client,
		keys:      set.NewSet[string](int(util.DefaultSeedSliceSize)),
	}
}

// mkContractCodeKey returns a LedgerKey of type ContractCode whose Hash differs by `tag`
// so tests can build distinct keys with trivial hand-writable input.
func mkContractCodeKey(tag byte) xdr.LedgerKey {
	h := xdr.Hash{}
	h[0] = tag
	return xdr.LedgerKey{
		Type:         xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.LedgerKeyContractCode{Hash: h},
	}
}

// mkResultMetaXDR builds a base64-encoded TransactionMeta V0 whose single operation
// contains a Removed change for each supplied key. Removed is the only change variant
// that carries a LedgerKey directly, so this avoids having to construct full
// LedgerEntry values with account sequences, balances, etc.
func mkResultMetaXDR(t *testing.T, keys ...xdr.LedgerKey) string {
	t.Helper()
	changes := make(xdr.LedgerEntryChanges, len(keys))
	for i := range keys {
		k := keys[i] // local copy so &k points at a distinct value per iteration
		changes[i] = xdr.LedgerEntryChange{
			Type:    xdr.LedgerEntryChangeTypeLedgerEntryRemoved,
			Removed: &k,
		}
	}
	ops := []xdr.OperationMeta{{Changes: changes}}
	meta := xdr.TransactionMeta{V: 0, Operations: &ops}
	b64, err := xdr.MarshalBase64(meta)
	require.NoError(t, err)
	return b64
}

func stubTxWithMeta(ledger uint32, hash, resultMetaXDR string) protocol.TransactionInfo {
	return protocol.TransactionInfo{
		TransactionDetails: protocol.TransactionDetails{
			Status:          "SUCCESS",
			TransactionHash: hash,
			Ledger:          ledger,
			ResultMetaXDR:   resultMetaXDR,
		},
	}
}

// TestLedgerKeySeeder_ExtractsSupportedKeys verifies that LedgerKeys present in a
// transaction's ResultMetaXDR are decoded and accumulated.
func TestLedgerKeySeeder_ExtractsOnlySupportedKeys(t *testing.T) {
	keyA := mkContractCodeKey(0xAA)
	keyB := mkContractCodeKey(0xBB)
	expectA, err := xdr.MarshalBase64(keyA)
	require.NoError(t, err)
	expectB, err := xdr.MarshalBase64(keyB)
	require.NoError(t, err)

	// Unsupported key -- should be dropped in seeded key set
	ttlHash := xdr.Hash{}
	ttlHash[0] = 0xEE
	ttlKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeTtl,
		Ttl:  &xdr.LedgerKeyTtl{KeyHash: ttlHash},
	}

	page := 0
	client, _ := captureTransactions(t, func() protocol.GetTransactionsResponse {
		page++
		if page == 1 {
			return protocol.GetTransactionsResponse{
				Transactions: []protocol.TransactionInfo{
					stubTxWithMeta(100, "h1", mkResultMetaXDR(t, keyA, keyB, ttlKey)),
				},
				Cursor: "toid-1",
			}
		}
		return protocol.GetTransactionsResponse{}
	})
	seeder := newTestLedgerKeySeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 100, Last: 100}))
	require.Contains(t, seeder.keys.Slice(), expectA)
	require.Contains(t, seeder.keys.Slice(), expectB)
	require.NotContains(t, seeder.keys.Slice(), ttlKey, "ttlKey should be filtered!")
}

// TestLedgerKeySeeder_Deduplicates verifies that the same key appearing in multiple
// transactions collapses to a single entry (accumulator is a set keyed by XDR bytes).
func TestLedgerKeySeeder_Deduplicates(t *testing.T) {
	key := mkContractCodeKey(0xDD)
	expect, err := xdr.MarshalBase64(key)
	require.NoError(t, err)

	page := 0
	client, _ := captureTransactions(t, func() protocol.GetTransactionsResponse {
		page++
		if page == 1 {
			// Same key emitted from two distinct txs AND twice within one tx's meta.
			return protocol.GetTransactionsResponse{
				Transactions: []protocol.TransactionInfo{
					stubTxWithMeta(300, "h1", mkResultMetaXDR(t, key)),
					stubTxWithMeta(300, "h2", mkResultMetaXDR(t, key, key)),
				},
				Cursor: "toid-1",
			}
		}
		return protocol.GetTransactionsResponse{}
	})
	seeder := newTestLedgerKeySeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 300, Last: 300}))
	require.Equal(t, []string{expect}, seeder.keys.Slice())
}
