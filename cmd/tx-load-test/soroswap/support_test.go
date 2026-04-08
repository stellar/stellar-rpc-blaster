package soroswap

import (
	"bytes"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestAddressScValSupportsAccountAndContractAddresses(t *testing.T) {
	kp, err := keypair.Random()
	require.NoError(t, err)

	accountVal, err := AddressScVal(kp.Address())
	require.NoError(t, err)
	require.Equal(t, xdr.ScValTypeScvAddress, accountVal.Type)
	require.Equal(t, xdr.ScAddressTypeScAddressTypeAccount, accountVal.Address.Type)

	contractAddress, err := strkey.Encode(strkey.VersionByteContract, bytes.Repeat([]byte{0x42}, 32))
	require.NoError(t, err)

	contractVal, err := AddressScVal(contractAddress)
	require.NoError(t, err)
	require.Equal(t, xdr.ScValTypeScvAddress, contractVal.Type)
	require.Equal(t, xdr.ScAddressTypeScAddressTypeContract, contractVal.Address.Type)
}

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
