package benchmark

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestChunkStrings(t *testing.T) {
	require.Equal(t, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}, chunkStrings([]string{"a", "b", "c", "d", "e"}, 2))
	require.Equal(t, [][]string{{"a", "b", "c"}}, chunkStrings([]string{"a", "b", "c"}, 3))
	require.Equal(t, [][]string{{"a", "b", "c"}}, chunkStrings([]string{"a", "b", "c"}, 10))
	require.Nil(t, chunkStrings(nil, 5))
	require.Empty(t, chunkStrings([]string{}, 5))
	// size <= 0 collapses to a single chunk rather than spinning forever.
	require.Equal(t, [][]string{{"a", "b"}}, chunkStrings([]string{"a", "b"}, 0))
}

func TestAccountLedgerKeyMatchesDecodedAccount(t *testing.T) {
	kp := keypair.MustRandom()

	key, err := accountLedgerKey(kp.Address())
	require.NoError(t, err)
	require.NotEmpty(t, key)

	var lk xdr.LedgerKey
	require.NoError(t, xdr.SafeUnmarshalBase64(key, &lk))
	require.Equal(t, xdr.LedgerEntryTypeAccount, lk.Type)
	require.NotNil(t, lk.Account)
	require.Equal(t, kp.Address(), lk.Account.AccountId.Address())

	// Encoding must be deterministic so KeyXDR echoed by stellar-rpc matches
	// the key we built and indexed by.
	again, err := accountLedgerKey(kp.Address())
	require.NoError(t, err)
	require.Equal(t, key, again)
}

func TestAccountLedgerKeyRejectsInvalidAddress(t *testing.T) {
	_, err := accountLedgerKey("not-a-valid-address")
	require.Error(t, err)
}

func TestAccountSeqNumFromEntryXDR(t *testing.T) {
	kp := keypair.MustRandom()
	accountID := xdr.MustAddress(kp.Address())

	entry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.AccountEntry{
			AccountId: accountID,
			SeqNum:    xdr.SequenceNumber(987654321),
		},
	}
	encoded, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)

	seq, err := accountSeqNumFromEntryXDR(encoded)
	require.NoError(t, err)
	require.Equal(t, int64(987654321), seq)
}

func TestAccountSeqNumFromEntryXDRRejectsNonAccount(t *testing.T) {
	_, err := accountSeqNumFromEntryXDR("")
	require.Error(t, err)

	// A non-account entry (e.g. TTL) must be rejected rather than silently
	// returning a zero sequence.
	entry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeTtl,
		Ttl:  &xdr.TtlEntry{LiveUntilLedgerSeq: 42},
	}
	encoded, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)

	_, err = accountSeqNumFromEntryXDR(encoded)
	require.Error(t, err)
}
