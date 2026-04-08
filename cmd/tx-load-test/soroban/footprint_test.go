package soroban

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func mustAccountID(t *testing.T, address string) xdr.AccountId {
	t.Helper()
	accountID, err := xdr.AddressToAccountId(address)
	require.NoError(t, err)
	return accountID
}

func TestReplaceFootprintReadWriteKeysReplacesAndClones(t *testing.T) {
	roAccount, err := keypair.Random()
	require.NoError(t, err)
	rwA, err := keypair.Random()
	require.NoError(t, err)
	rwB, err := keypair.Random()
	require.NoError(t, err)

	roID := mustAccountID(t, roAccount.Address())
	rwAID := mustAccountID(t, rwA.Address())
	rwBID := mustAccountID(t, rwB.Address())
	tmpl := xdr.LedgerFootprint{
		ReadOnly: []xdr.LedgerKey{{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: roID}}},
		ReadWrite: []xdr.LedgerKey{{
			Type:    xdr.LedgerEntryTypeAccount,
			Account: &xdr.LedgerKeyAccount{AccountId: roID},
		}},
	}
	replacement := []xdr.LedgerKey{
		{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: rwAID}},
		{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: rwBID}},
	}

	footprint := ReplaceFootprintReadWriteKeys(tmpl, replacement...)

	require.Equal(t, tmpl.ReadOnly, footprint.ReadOnly)
	require.Equal(t, replacement, footprint.ReadWrite)
	require.NotSame(t, &tmpl.ReadOnly[0], &footprint.ReadOnly[0])
	require.NotSame(t, &replacement[0], &footprint.ReadWrite[0])
}
