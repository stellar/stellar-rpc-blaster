package benchmark

import (
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	soroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
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

func mustTrustlineKey(t *testing.T, asset xdr.Asset, address string) xdr.LedgerKey {
	t.Helper()
	key, err := trustlineLedgerKey(mustAccountID(t, address), asset)
	require.NoError(t, err)
	return key
}

func mustLedgerKeyBase64(t *testing.T, key xdr.LedgerKey) string {
	t.Helper()
	encoded, err := xdr.MarshalBase64(key)
	require.NoError(t, err)
	return encoded
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

func TestBuildSoroswapFootprintAddsTraderTrustlines(t *testing.T) {
	oldTrader, err := keypair.Random()
	require.NoError(t, err)
	newTrader, err := keypair.Random()
	require.NoError(t, err)
	readOnlyAccount, err := keypair.Random()
	require.NoError(t, err)

	contractID := xdr.ContractId{1}
	assetA := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'U', 'S', 'D'},
			Issuer:    mustAccountID(t, readOnlyAccount.Address()),
		},
	}
	assetB := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'E', 'U', 'R'},
			Issuer:    mustAccountID(t, oldTrader.Address()),
		},
	}
	tmpl := xdr.LedgerFootprint{
		ReadOnly:  []xdr.LedgerKey{{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: mustAccountID(t, readOnlyAccount.Address())}}},
		ReadWrite: []xdr.LedgerKey{mustBalanceKey(t, contractID, oldTrader.Address())},
	}

	footprint, err := buildSoroswapFootprint(tmpl, oldTrader.Address(), newTrader.Address(), assetA, assetB)
	require.NoError(t, err)
	require.Equal(t, tmpl.ReadOnly, footprint.ReadOnly)
	require.Len(t, footprint.ReadWrite, 3)

	encodedKeys := []string{
		mustLedgerKeyBase64(t, footprint.ReadWrite[0]),
		mustLedgerKeyBase64(t, footprint.ReadWrite[1]),
		mustLedgerKeyBase64(t, footprint.ReadWrite[2]),
	}
	require.Contains(t, encodedKeys, mustLedgerKeyBase64(t, mustBalanceKey(t, contractID, newTrader.Address())))
	require.Contains(t, encodedKeys, mustLedgerKeyBase64(t, mustTrustlineKey(t, assetA, newTrader.Address())))
	require.Contains(t, encodedKeys, mustLedgerKeyBase64(t, mustTrustlineKey(t, assetB, newTrader.Address())))
}

func TestBuildSoroswapFootprintDoesNotDuplicateTrustlines(t *testing.T) {
	oldTrader, err := keypair.Random()
	require.NoError(t, err)
	newTrader, err := keypair.Random()
	require.NoError(t, err)
	issuer, err := keypair.Random()
	require.NoError(t, err)

	asset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'U', 'S', 'D'},
			Issuer:    mustAccountID(t, issuer.Address()),
		},
	}
	tmpl := xdr.LedgerFootprint{
		ReadWrite: []xdr.LedgerKey{mustTrustlineKey(t, asset, oldTrader.Address())},
	}

	footprint, err := buildSoroswapFootprint(tmpl, oldTrader.Address(), newTrader.Address(), asset, asset)
	require.NoError(t, err)
	require.Len(t, footprint.ReadWrite, 1)
	require.Equal(t, mustTrustlineKey(t, asset, newTrader.Address()), footprint.ReadWrite[0])
}

func TestSoroswapResourcesForFootprintAddsManualHeadroom(t *testing.T) {
	simulated := simulatedInvocationTemplate{simulation: sharedTestSimulatedInvocation(
		xdr.SorobanResources{
			Footprint:     xdr.LedgerFootprint{ReadWrite: make([]xdr.LedgerKey, 5)},
			Instructions:  4_823_792,
			DiskReadBytes: 243,
			WriteBytes:    1_247,
		},
		47_484,
	)}
	footprint := xdr.LedgerFootprint{ReadWrite: make([]xdr.LedgerKey, 7)}

	resources, fee := soroswapResourcesForFootprint(simulated.simulation, footprint)
	require.Equal(t, footprint, resources.Footprint)
	require.Equal(t, xdr.Uint32(243+2*soroswapAdditionalDiskReadBytesPerKey), resources.DiskReadBytes)
	require.Equal(t, xdr.Uint32(1247+2*soroswapAdditionalWriteBytesPerKey), resources.WriteBytes)
	require.Equal(t, xdr.Int64(47484+2*soroswapAdditionalResourceFeePerKey), fee)
}

func TestSoroswapResourcesForFootprintKeepsOriginalBudgetWithoutNewKeys(t *testing.T) {
	original := xdr.LedgerFootprint{ReadWrite: make([]xdr.LedgerKey, 5)}
	simulated := simulatedInvocationTemplate{simulation: sharedTestSimulatedInvocation(
		xdr.SorobanResources{
			Footprint:     original,
			Instructions:  10,
			DiskReadBytes: 20,
			WriteBytes:    30,
		},
		40,
	)}

	resources, fee := soroswapResourcesForFootprint(simulated.simulation, original)
	require.Equal(t, original, resources.Footprint)
	require.Equal(t, xdr.Uint32(20), resources.DiskReadBytes)
	require.Equal(t, xdr.Uint32(30), resources.WriteBytes)
	require.Equal(t, xdr.Int64(40), fee)
}

func sharedTestSimulatedInvocation(resources xdr.SorobanResources, fee xdr.Int64) soroban.SimulatedInvocation {
	return soroban.SimulatedInvocation{
		Resources:   resources,
		ResourceFee: fee,
		Footprint:   resources.Footprint,
	}
}
