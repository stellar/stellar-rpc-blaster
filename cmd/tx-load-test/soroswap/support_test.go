package soroswap

import (
	"sync"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestRewriteScValAccountRewritesNestedStructures(t *testing.T) {
	oldKP, err := keypair.Random()
	require.NoError(t, err)
	newKP, err := keypair.Random()
	require.NoError(t, err)

	oldAccountID, err := xdr.AddressToAccountId(oldKP.Address())
	require.NoError(t, err)
	value := xdr.ScVal{Type: xdr.ScValTypeScvVec}
	innerVec := xdr.ScVec{
		{
			Type: xdr.ScValTypeScvMap,
			Map: func() **xdr.ScMap {
				m := xdr.ScMap{{
					Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: func() *xdr.ScSymbol { s := xdr.ScSymbol("owner"); return &s }()},
					Val: xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &oldAccountID}},
				}}
				return func() **xdr.ScMap { ref := &m; return &ref }()
			}(),
		},
	}
	value.Vec = func() **xdr.ScVec { ref := &innerVec; return &ref }()

	rewritten, err := RewriteScValAccount(value, oldKP.Address(), newKP.Address())
	require.NoError(t, err)
	require.NotNil(t, rewritten.Vec)
	require.NotNil(t, *rewritten.Vec)
	require.Len(t, **rewritten.Vec, 1)

	rewrittenMapVal := (**rewritten.Vec)[0]
	require.NotNil(t, rewrittenMapVal.Map)
	require.NotNil(t, *rewrittenMapVal.Map)
	require.Len(t, **rewrittenMapVal.Map, 1)
	rewrittenAddress := (**rewrittenMapVal.Map)[0].Val.Address
	require.NotNil(t, rewrittenAddress)
	require.Equal(t, newKP.Address(), rewrittenAddress.AccountId.Address())
}

func TestRewriteFootprintAccountRewritesAccountAndTrustlineKeys(t *testing.T) {
	oldKP, err := keypair.Random()
	require.NoError(t, err)
	newKP, err := keypair.Random()
	require.NoError(t, err)
	oldAccountID, err := xdr.AddressToAccountId(oldKP.Address())
	require.NoError(t, err)

	footprint := xdr.LedgerFootprint{
		ReadOnly: []xdr.LedgerKey{{
			Type:    xdr.LedgerEntryTypeAccount,
			Account: &xdr.LedgerKeyAccount{AccountId: oldAccountID},
		}},
		ReadWrite: []xdr.LedgerKey{{
			Type: xdr.LedgerEntryTypeTrustline,
			TrustLine: &xdr.LedgerKeyTrustLine{
				AccountId: oldAccountID,
			},
		}},
	}

	rewritten, err := RewriteFootprintAccount(footprint, oldKP.Address(), newKP.Address())
	require.NoError(t, err)
	require.Equal(t, newKP.Address(), rewritten.ReadOnly[0].Account.AccountId.Address())
	require.Equal(t, newKP.Address(), rewritten.ReadWrite[0].TrustLine.AccountId.Address())
}

func TestRewriteFootprintAccountDoesNotMutateTemplate(t *testing.T) {
	simKP, err := keypair.Random()
	require.NoError(t, err)
	traderA, err := keypair.Random()
	require.NoError(t, err)
	traderB, err := keypair.Random()
	require.NoError(t, err)

	template := newRewriteTestTemplate(t, simKP.Address())
	templateBefore, err := template.MarshalBinary()
	require.NoError(t, err)

	for _, trader := range []*keypair.Full{traderA, traderB} {
		rewritten, err := RewriteFootprintAccount(template, simKP.Address(), trader.Address())
		require.NoError(t, err)
		requireRewrittenTemplate(t, template, rewritten, trader.Address())
	}

	templateAfter, err := template.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, templateBefore, templateAfter)
}

// Run with -race: targeters rewrite the same template from many goroutines.
func TestRewriteFootprintAccountConcurrentSharedTemplate(t *testing.T) {
	simKP, err := keypair.Random()
	require.NoError(t, err)

	template := newRewriteTestTemplate(t, simKP.Address())
	templateBefore, err := template.MarshalBinary()
	require.NoError(t, err)

	const workers = 8
	traders := make([]string, workers)
	for i := range traders {
		kp, err := keypair.Random()
		require.NoError(t, err)
		traders[i] = kp.Address()
	}

	results := make([]xdr.LedgerFootprint, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = RewriteFootprintAccount(template, simKP.Address(), traders[i])
		}()
	}
	wg.Wait()

	for i := range workers {
		require.NoError(t, errs[i])
		requireRewrittenTemplate(t, template, results[i], traders[i])
	}
	templateAfter, err := template.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, templateBefore, templateAfter)
}

// newRewriteTestTemplate returns a footprint that references simAddress
// through each LedgerKey arm rewriteLedgerKeyAccount handles (Account,
// TrustLine, ContractData), plus a contract-instance key that doesn't mention
// the trader. requireRewrittenTemplate relies on this key order.
func newRewriteTestTemplate(t *testing.T, simAddress string) xdr.LedgerFootprint {
	t.Helper()
	simAccountID, err := xdr.AddressToAccountId(simAddress)
	require.NoError(t, err)
	issuer, err := keypair.Random()
	require.NoError(t, err)
	asset, err := xdr.NewCreditAsset("USDC", issuer.Address())
	require.NoError(t, err)
	var contractID xdr.ContractId
	contractID[0] = 1
	contract := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID}

	return xdr.LedgerFootprint{
		ReadOnly: []xdr.LedgerKey{
			{
				Type:    xdr.LedgerEntryTypeAccount,
				Account: &xdr.LedgerKeyAccount{AccountId: simAccountID},
			},
			{
				Type: xdr.LedgerEntryTypeContractData,
				ContractData: &xdr.LedgerKeyContractData{
					Contract:   contract,
					Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
					Durability: xdr.ContractDataDurabilityPersistent,
				},
			},
		},
		ReadWrite: []xdr.LedgerKey{
			{
				Type: xdr.LedgerEntryTypeTrustline,
				TrustLine: &xdr.LedgerKeyTrustLine{
					AccountId: simAccountID,
					Asset:     asset.ToTrustLineAsset(),
				},
			},
			{
				Type: xdr.LedgerEntryTypeContractData,
				ContractData: &xdr.LedgerKeyContractData{
					Contract:   contract,
					Key:        xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &simAccountID}},
					Durability: xdr.ContractDataDurabilityPersistent,
				},
			},
		},
	}
}

// requireRewrittenTemplate checks that rewritten is template with the trader
// swapped in, and that every rewritten arm is a new object rather than the
// template's own pointer.
func requireRewrittenTemplate(t *testing.T, template, rewritten xdr.LedgerFootprint, trader string) {
	t.Helper()
	require.Len(t, rewritten.ReadOnly, len(template.ReadOnly))
	require.Len(t, rewritten.ReadWrite, len(template.ReadWrite))

	account := rewritten.ReadOnly[0].Account
	require.Equal(t, trader, account.AccountId.Address())
	require.NotSame(t, template.ReadOnly[0].Account, account)

	require.Equal(t, template.ReadOnly[1], rewritten.ReadOnly[1])

	trustLine := rewritten.ReadWrite[0].TrustLine
	require.Equal(t, trader, trustLine.AccountId.Address())
	require.Equal(t, template.ReadWrite[0].TrustLine.Asset, trustLine.Asset)
	require.NotSame(t, template.ReadWrite[0].TrustLine, trustLine)

	contractData := rewritten.ReadWrite[1].ContractData
	require.Equal(t, trader, contractData.Key.Address.AccountId.Address())
	require.Equal(t, template.ReadWrite[1].ContractData.Contract, contractData.Contract)
	require.Equal(t, template.ReadWrite[1].ContractData.Durability, contractData.Durability)
	require.NotSame(t, template.ReadWrite[1].ContractData, contractData)
}
