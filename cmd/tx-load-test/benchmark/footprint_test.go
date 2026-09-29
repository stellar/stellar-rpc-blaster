package benchmark

import (
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

func mustAccountID(t *testing.T, address string) xdr.AccountId {
	t.Helper()
	accountID, err := xdr.AddressToAccountId(address)
	require.NoError(t, err)
	return accountID
}

func mustBalanceKey(t *testing.T, contractID xdr.ContractId, address string) xdr.LedgerKey {
	t.Helper()
	key, err := ledger.OZBalanceLedgerKey(contractID, address)
	require.NoError(t, err)
	return key
}

func TestBuildFootprintFromTemplateReplacesReadWriteKeys(t *testing.T) {
	account, err := keypair.Random()
	require.NoError(t, err)
	tmpl := xdr.LedgerFootprint{
		ReadOnly: []xdr.LedgerKey{{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: mustAccountID(t, account.Address())}}},
	}
	first := xdr.LedgerKey{Type: xdr.LedgerEntryTypeContractData}
	second := xdr.LedgerKey{Type: xdr.LedgerEntryTypeContractCode}

	footprint, err := buildFootprintFromTemplate(
		tmpl,
		func() (xdr.LedgerKey, error) { return first, nil },
		func() (xdr.LedgerKey, error) { return second, nil },
	)
	require.NoError(t, err)
	require.Equal(t, tmpl.ReadOnly, footprint.ReadOnly)
	require.Equal(t, []xdr.LedgerKey{first, second}, footprint.ReadWrite)
}

func TestBuildFootprintFromTemplateReturnsBuilderError(t *testing.T) {
	wantErr := errors.New("boom")

	_, err := buildFootprintFromTemplate(
		xdr.LedgerFootprint{},
		func() (xdr.LedgerKey, error) { return xdr.LedgerKey{}, nil },
		func() (xdr.LedgerKey, error) { return xdr.LedgerKey{}, wantErr },
	)
	require.ErrorIs(t, err, wantErr)
}

func TestBuildSACFootprintFromTemplateReplacesTrustlineKeys(t *testing.T) {
	issuer, err := keypair.Random()
	require.NoError(t, err)
	src, err := keypair.Random()
	require.NoError(t, err)
	dst, err := keypair.Random()
	require.NoError(t, err)

	issuerID := mustAccountID(t, issuer.Address())
	srcID := mustAccountID(t, src.Address())
	dstID := mustAccountID(t, dst.Address())
	tmpl := xdr.LedgerFootprint{
		ReadOnly: []xdr.LedgerKey{{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: issuerID}}},
	}
	asset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'U', 'S', 'D'},
			Issuer:    issuerID,
		},
	}

	footprint, err := buildSACFootprintFromTemplate(tmpl, asset, srcID, dstID)
	require.NoError(t, err)

	require.Equal(t, tmpl.ReadOnly, footprint.ReadOnly)
	require.Len(t, footprint.ReadWrite, 2)
	require.Equal(t, srcID, footprint.ReadWrite[0].TrustLine.AccountId)
	require.Equal(t, dstID, footprint.ReadWrite[1].TrustLine.AccountId)
}

func TestBuildOZFootprintFromTemplateReplacesBalanceKeys(t *testing.T) {
	src, err := keypair.Random()
	require.NoError(t, err)
	dst, err := keypair.Random()
	require.NoError(t, err)
	readOnlyAccount, err := keypair.Random()
	require.NoError(t, err)

	readOnlyID := mustAccountID(t, readOnlyAccount.Address())
	contractID := xdr.ContractId{1}
	tmpl := xdr.LedgerFootprint{
		ReadOnly: []xdr.LedgerKey{{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: readOnlyID}}},
	}

	footprint, err := buildOZFootprintFromTemplate(tmpl, contractID, src.Address(), dst.Address())
	require.NoError(t, err)

	expectedSrcKey := mustBalanceKey(t, contractID, src.Address())
	expectedDstKey := mustBalanceKey(t, contractID, dst.Address())
	require.Equal(t, tmpl.ReadOnly, footprint.ReadOnly)
	require.Equal(t, []xdr.LedgerKey{expectedSrcKey, expectedDstKey}, footprint.ReadWrite)
}

func TestBuildOZFootprintFromTemplateReportsKeyErrors(t *testing.T) {
	contractID := xdr.ContractId{1}
	_, err := buildOZFootprintFromTemplate(xdr.LedgerFootprint{}, contractID, "not-an-address", "also-bad")
	require.Error(t, err)
	require.ErrorContains(t, err, "src balance key")
}
