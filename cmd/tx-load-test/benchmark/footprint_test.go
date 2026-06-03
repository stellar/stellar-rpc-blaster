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

	footprint, ext, err := buildSACFootprintFromTemplate(tmpl, xdr.SorobanTransactionDataExt{}, asset, srcID, dstID)
	require.NoError(t, err)

	require.Equal(t, tmpl.ReadOnly, footprint.ReadOnly)
	require.Len(t, footprint.ReadWrite, 2)
	require.Equal(t, srcID, footprint.ReadWrite[0].TrustLine.AccountId)
	require.Equal(t, dstID, footprint.ReadWrite[1].TrustLine.AccountId)
	require.Equal(t, int32(0), int32(ext.V))
}

func TestBuildSACFootprintFromTemplatePreservesNonTrustlineReadWriteKeys(t *testing.T) {
	issuer, err := keypair.Random()
	require.NoError(t, err)
	templateSrc, err := keypair.Random()
	require.NoError(t, err)
	templateDst, err := keypair.Random()
	require.NoError(t, err)
	src, err := keypair.Random()
	require.NoError(t, err)
	dst, err := keypair.Random()
	require.NoError(t, err)

	issuerID := mustAccountID(t, issuer.Address())
	asset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'U', 'S', 'D'},
			Issuer:    issuerID,
		},
	}
	contractInstanceKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &xdr.ContractId{42},
			},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}
	tmpl := xdr.LedgerFootprint{
		ReadWrite: []xdr.LedgerKey{
			contractInstanceKey,
			mustTrustlineKey(t, asset, templateSrc.Address()),
			mustTrustlineKey(t, asset, templateDst.Address()),
		},
	}

	footprint, ext, err := buildSACFootprintFromTemplate(tmpl, xdr.SorobanTransactionDataExt{}, asset, mustAccountID(t, src.Address()), mustAccountID(t, dst.Address()))
	require.NoError(t, err)

	require.Equal(t, []xdr.LedgerKey{
		contractInstanceKey,
		mustTrustlineKey(t, asset, src.Address()),
		mustTrustlineKey(t, asset, dst.Address()),
	}, footprint.ReadWrite)
	require.Equal(t, int32(0), int32(ext.V))
}

// TestBuildSACFootprintFromTemplateFiltersDespitePointerInequality is a
// regression for the bug where Go's `==` on AlphaNum4 compares the
// Issuer.Ed25519 pointer rather than the 32 bytes it points at. The
// simulator's per-tx XDR responses carry freshly-allocated pointers, so the
// old struct-equality compare returned false for assets that were logically
// identical -- the filter never matched and every per-asset trustline from
// the template survived alongside the appended per-request trustlines,
// producing core's "duplicate key in the Soroban footprint" rejection.
func TestBuildSACFootprintFromTemplateFiltersDespitePointerInequality(t *testing.T) {
	issuer, err := keypair.Random()
	require.NoError(t, err)
	templateHolder, err := keypair.Random()
	require.NoError(t, err)
	actualSrc, err := keypair.Random()
	require.NoError(t, err)
	actualDst, err := keypair.Random()
	require.NoError(t, err)

	// Build two AlphaNum4 values that are logically identical but use
	// distinct Issuer pointer instances -- exactly the shape the bug
	// produced. Each AddressToAccountId call mallocs a new *Uint256.
	templateIssuerID, err := xdr.AddressToAccountId(issuer.Address())
	require.NoError(t, err)
	runtimeIssuerID, err := xdr.AddressToAccountId(issuer.Address())
	require.NoError(t, err)
	templateAsset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'B', 'L', 'T', 'C'},
			Issuer:    templateIssuerID,
		},
	}
	runtimeAsset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'B', 'L', 'T', 'C'},
			Issuer:    runtimeIssuerID,
		},
	}
	// Sanity: prove the two AlphaNum4 use distinct Issuer.Ed25519 pointer
	// instances. Go's `==` walks the embedded AccountId.PublicKey and
	// compares Ed25519 by pointer address rather than by 32-byte value, so
	// `*templateAsset.AlphaNum4 == *runtimeAsset.AlphaNum4` was the
	// originally-broken comparison path. require.NotEqual uses
	// reflect.DeepEqual (which dereferences) so we assert pointer
	// inequality directly here.
	require.NotSame(t, templateAsset.AlphaNum4.Issuer.Ed25519, runtimeAsset.AlphaNum4.Issuer.Ed25519,
		"test fixture must use distinct *Uint256 instances to exercise the pointer-compare regression")

	tmpl := xdr.LedgerFootprint{
		ReadWrite: []xdr.LedgerKey{
			mustTrustlineKey(t, templateAsset, templateHolder.Address()),
		},
	}
	footprint, _, err := buildSACFootprintFromTemplate(
		tmpl,
		xdr.SorobanTransactionDataExt{},
		runtimeAsset,
		mustAccountID(t, actualSrc.Address()),
		mustAccountID(t, actualDst.Address()),
	)
	require.NoError(t, err)
	require.Len(t, footprint.ReadWrite, 2,
		"template's BLTC trustline must be filtered out before appending the actual src/dst trustlines")
	require.Equal(t, mustAccountID(t, actualSrc.Address()), footprint.ReadWrite[0].TrustLine.AccountId)
	require.Equal(t, mustAccountID(t, actualDst.Address()), footprint.ReadWrite[1].TrustLine.AccountId)
}

// TestBuildSACFootprintFromTemplateRemapsArchivedSorobanEntries verifies that
// autorestore indices are translated after the per-asset trustlines are
// filtered out and the actual src/dst trustlines are appended. Without
// remapping, the original archived index would either point off the end of
// the new RW (out-of-bounds at core's submit validation) or at a classic
// trustline (non-persistent error).
func TestBuildSACFootprintFromTemplateRemapsArchivedSorobanEntries(t *testing.T) {
	issuer, err := keypair.Random()
	require.NoError(t, err)
	templateHolder0, err := keypair.Random()
	require.NoError(t, err)
	templateHolder1, err := keypair.Random()
	require.NoError(t, err)
	actualSrc, err := keypair.Random()
	require.NoError(t, err)
	actualDst, err := keypair.Random()
	require.NoError(t, err)

	asset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'B', 'L', 'T', 'C'},
			Issuer:    mustAccountID(t, issuer.Address()),
		},
	}
	contractInstanceKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &xdr.ContractId{42},
			},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}
	// Simulator output shape when the SAC contract instance is archived:
	// holder trustlines come first (RW for the transfer) and the instance
	// entry is appended (RW because it needs auto-restoration). The
	// archived index points at the instance entry at position 2.
	tmpl := xdr.LedgerFootprint{
		ReadWrite: []xdr.LedgerKey{
			mustTrustlineKey(t, asset, templateHolder0.Address()),
			mustTrustlineKey(t, asset, templateHolder1.Address()),
			contractInstanceKey,
		},
	}
	tmplExt := xdr.SorobanTransactionDataExt{
		V: 1,
		ResourceExt: &xdr.SorobanResourcesExtV0{
			ArchivedSorobanEntries: []xdr.Uint32{2},
		},
	}

	footprint, ext, err := buildSACFootprintFromTemplate(
		tmpl, tmplExt, asset,
		mustAccountID(t, actualSrc.Address()),
		mustAccountID(t, actualDst.Address()),
	)
	require.NoError(t, err)

	// After filter + append: [contractInstance, actualSrcTrust, actualDstTrust].
	require.Equal(t, []xdr.LedgerKey{
		contractInstanceKey,
		mustTrustlineKey(t, asset, actualSrc.Address()),
		mustTrustlineKey(t, asset, actualDst.Address()),
	}, footprint.ReadWrite)

	// The archived index for the contract instance must move from 2 (its
	// position in the template) to 0 (its position in the rewrite).
	require.Equal(t, int32(1), int32(ext.V))
	require.NotNil(t, ext.ResourceExt)
	require.Equal(t, []xdr.Uint32{0}, ext.ResourceExt.ArchivedSorobanEntries)
}

// TestBuildSACFootprintFromTemplateDropsArchivedIndicesForFilteredEntries
// verifies that archived indices pointing at template entries that get
// dropped during the rewrite (per-asset trustlines) are silently filtered
// from the returned extension. Classic trustlines aren't persistent and
// can't be auto-restored anyway -- carrying a stale index would trip core's
// "archivedSorobanEntries index points to a non-persistent entry" check.
func TestBuildSACFootprintFromTemplateDropsArchivedIndicesForFilteredEntries(t *testing.T) {
	issuer, err := keypair.Random()
	require.NoError(t, err)
	templateHolder, err := keypair.Random()
	require.NoError(t, err)
	actualSrc, err := keypair.Random()
	require.NoError(t, err)
	actualDst, err := keypair.Random()
	require.NoError(t, err)

	asset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'B', 'L', 'T', 'C'},
			Issuer:    mustAccountID(t, issuer.Address()),
		},
	}
	tmpl := xdr.LedgerFootprint{
		ReadWrite: []xdr.LedgerKey{
			mustTrustlineKey(t, asset, templateHolder.Address()),
		},
	}
	// Spurious archived index pointing at a trustline that the rewrite
	// drops. Should be filtered out and the extension downgraded to V0.
	tmplExt := xdr.SorobanTransactionDataExt{
		V: 1,
		ResourceExt: &xdr.SorobanResourcesExtV0{
			ArchivedSorobanEntries: []xdr.Uint32{0},
		},
	}
	_, ext, err := buildSACFootprintFromTemplate(
		tmpl, tmplExt, asset,
		mustAccountID(t, actualSrc.Address()),
		mustAccountID(t, actualDst.Address()),
	)
	require.NoError(t, err)
	require.Equal(t, int32(0), int32(ext.V))
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

	repSrcKey := mustBalanceKey(t, contractID, src.Address())
	repDstKey := mustBalanceKey(t, contractID, dst.Address())
	footprint, _, err := buildOZFootprintFromTemplate(
		tmpl, xdr.SorobanTransactionDataExt{}, contractID,
		src.Address(), dst.Address(), repSrcKey, repDstKey,
	)
	require.NoError(t, err)

	expectedSrcKey := mustBalanceKey(t, contractID, src.Address())
	expectedDstKey := mustBalanceKey(t, contractID, dst.Address())
	require.Equal(t, tmpl.ReadOnly, footprint.ReadOnly)
	require.Equal(t, []xdr.LedgerKey{expectedSrcKey, expectedDstKey}, footprint.ReadWrite)
}

func TestBuildOZFootprintFromTemplateReportsKeyErrors(t *testing.T) {
	contractID := xdr.ContractId{1}
	repSrc := xdr.LedgerKey{Type: xdr.LedgerEntryTypeAccount}
	repDst := xdr.LedgerKey{Type: xdr.LedgerEntryTypeAccount}
	_, _, err := buildOZFootprintFromTemplate(
		xdr.LedgerFootprint{}, xdr.SorobanTransactionDataExt{}, contractID,
		"not-an-address", "also-bad", repSrc, repDst,
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "src balance key")
}

// TestBuildOZFootprintFromTemplateRemapsArchivedSorobanEntries: when the
// simulator's template RW contains the OZ contract instance entry (because
// the instance is archived and needs autorestore), the bench's rewrite
// drops the representative src/dst balance keys, keeps the instance entry,
// and remaps the archivedSorobanEntries indices accordingly. Without this,
// the index would point at one of the appended actual balance entries
// (which IS persistent, so it'd "work" but autorestore the wrong entry) or
// off the end of RW (out-of-bounds at submit).
func TestBuildOZFootprintFromTemplateRemapsArchivedSorobanEntries(t *testing.T) {
	repSrc, err := keypair.Random()
	require.NoError(t, err)
	repDst, err := keypair.Random()
	require.NoError(t, err)
	actualSrc, err := keypair.Random()
	require.NoError(t, err)
	actualDst, err := keypair.Random()
	require.NoError(t, err)
	contractID := xdr.ContractId{1}

	repSrcKey := mustBalanceKey(t, contractID, repSrc.Address())
	repDstKey := mustBalanceKey(t, contractID, repDst.Address())
	contractInstanceKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &xdr.ContractId{42},
			},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}
	tmpl := xdr.LedgerFootprint{
		ReadWrite: []xdr.LedgerKey{repSrcKey, repDstKey, contractInstanceKey},
	}
	tmplExt := xdr.SorobanTransactionDataExt{
		V: 1,
		ResourceExt: &xdr.SorobanResourcesExtV0{
			ArchivedSorobanEntries: []xdr.Uint32{2},
		},
	}

	footprint, ext, err := buildOZFootprintFromTemplate(
		tmpl, tmplExt, contractID,
		actualSrc.Address(), actualDst.Address(), repSrcKey, repDstKey,
	)
	require.NoError(t, err)

	expectedSrcKey := mustBalanceKey(t, contractID, actualSrc.Address())
	expectedDstKey := mustBalanceKey(t, contractID, actualDst.Address())
	require.Equal(t, []xdr.LedgerKey{contractInstanceKey, expectedSrcKey, expectedDstKey}, footprint.ReadWrite)
	require.Equal(t, int32(1), int32(ext.V))
	require.NotNil(t, ext.ResourceExt)
	require.Equal(t, []xdr.Uint32{0}, ext.ResourceExt.ArchivedSorobanEntries)
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

func TestCompareSoroswapBenchmarkFootprintsAllowsExtraTrustlines(t *testing.T) {
	trader, err := keypair.Random()
	require.NoError(t, err)
	issuer, err := keypair.Random()
	require.NoError(t, err)

	contractID := xdr.ContractId{1}
	asset := xdr.Asset{
		Type: xdr.AssetTypeAssetTypeCreditAlphanum4,
		AlphaNum4: &xdr.AlphaNum4{
			AssetCode: [4]byte{'U', 'S', 'D'},
			Issuer:    mustAccountID(t, issuer.Address()),
		},
	}
	simulated := xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{mustBalanceKey(t, contractID, trader.Address())}}
	benchmark := xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{
		mustBalanceKey(t, contractID, trader.Address()),
		mustTrustlineKey(t, asset, trader.Address()),
	}}

	comparison, err := compareSoroswapBenchmarkFootprints(simulated, benchmark)
	require.NoError(t, err)
	require.False(t, comparison.hasMismatch())
	require.Equal(t, 1, comparison.allowedExtraReadWriteKeys)
}

func TestCompareSoroswapBenchmarkFootprintsDetectsContractDataMismatch(t *testing.T) {
	oldTrader, err := keypair.Random()
	require.NoError(t, err)
	newTrader, err := keypair.Random()
	require.NoError(t, err)

	contractID := xdr.ContractId{1}
	simulated := xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{mustBalanceKey(t, contractID, newTrader.Address())}}
	benchmark := xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{mustBalanceKey(t, contractID, oldTrader.Address())}}

	comparison, err := compareSoroswapBenchmarkFootprints(simulated, benchmark)
	require.NoError(t, err)
	require.True(t, comparison.hasMismatch())
	require.Equal(t, 1, comparison.missingReadWriteKeys)
	require.Equal(t, 1, comparison.unexpectedReadWriteKeys)
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
