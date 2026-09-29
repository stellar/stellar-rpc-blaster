package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/xdr"
)

func testContractID(fill byte) xdr.ContractId {
	var id xdr.ContractId
	for i := range id {
		id[i] = fill
	}
	return id
}

func TestContractInstanceLedgerKey(t *testing.T) {
	contractID := testContractID(0xAA)
	key := ContractInstanceLedgerKey(contractID)

	require.Equal(t, xdr.LedgerEntryTypeContractData, key.Type)
	require.Equal(t, xdr.ScAddressTypeScAddressTypeContract, key.ContractData.Contract.Type)
	require.Equal(t, contractID, *key.ContractData.Contract.ContractId)
	require.Equal(t, xdr.ScValTypeScvLedgerKeyContractInstance, key.ContractData.Key.Type)
	require.Equal(t, xdr.ContractDataDurabilityPersistent, key.ContractData.Durability)

	// Must round-trip through XDR (getLedgerEntries keys are base64 XDR).
	b64, err := xdr.MarshalBase64(key)
	require.NoError(t, err)
	var decoded xdr.LedgerKey
	require.NoError(t, xdr.SafeUnmarshalBase64(b64, &decoded))
	require.Equal(t, contractID, *decoded.ContractData.Contract.ContractId)
}

func TestContractCodeLedgerKey(t *testing.T) {
	var hash xdr.Hash
	for i := range hash {
		hash[i] = byte(i)
	}
	key := ContractCodeLedgerKey(hash)
	require.Equal(t, xdr.LedgerEntryTypeContractCode, key.Type)
	require.Equal(t, hash, key.ContractCode.Hash)

	b64, err := xdr.MarshalBase64(key)
	require.NoError(t, err)
	var decoded xdr.LedgerKey
	require.NoError(t, xdr.SafeUnmarshalBase64(b64, &decoded))
	require.Equal(t, hash, decoded.ContractCode.Hash)
}

func TestContractBalanceLedgerKey(t *testing.T) {
	sacID := testContractID(0x01)
	holderID := testContractID(0x02)
	key := ContractBalanceLedgerKey(sacID, ContractScAddress(holderID))

	require.Equal(t, xdr.LedgerEntryTypeContractData, key.Type)
	require.Equal(t, sacID, *key.ContractData.Contract.ContractId)
	require.Equal(t, xdr.ContractDataDurabilityPersistent, key.ContractData.Durability)

	// Key must be Vec[Symbol("Balance"), Address(holder)].
	require.Equal(t, xdr.ScValTypeScvVec, key.ContractData.Key.Type)
	vec := **key.ContractData.Key.Vec
	require.Len(t, vec, 2)
	require.Equal(t, xdr.ScSymbol("Balance"), *vec[0].Sym)
	require.Equal(t, holderID, *vec[1].Address.ContractId)
}

// TestOZBalanceLedgerKeyMatchesGeneralBuilder pins the refactor: the
// account-address wrapper must produce the same XDR as the general builder
// with an account ScAddress.
func TestOZBalanceLedgerKeyMatchesGeneralBuilder(t *testing.T) {
	ozID := testContractID(0x0F)
	const address = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

	viaWrapper, err := OZBalanceLedgerKey(ozID, address)
	require.NoError(t, err)

	accountID, err := xdr.AddressToAccountId(address)
	require.NoError(t, err)
	viaGeneral := ContractBalanceLedgerKey(ozID, xdr.ScAddress{
		Type:      xdr.ScAddressTypeScAddressTypeAccount,
		AccountId: &accountID,
	})

	b1, err := xdr.MarshalBase64(viaWrapper)
	require.NoError(t, err)
	b2, err := xdr.MarshalBase64(viaGeneral)
	require.NoError(t, err)
	require.Equal(t, b1, b2)
}

func TestWasmHashFromInstance(t *testing.T) {
	var hash xdr.Hash
	for i := range hash {
		hash[i] = 0x5A
	}
	contractID := testContractID(0x03)

	makeInstance := func(exec xdr.ContractExecutable) *xdr.LedgerEntryData {
		return &xdr.LedgerEntryData{
			Type: xdr.LedgerEntryTypeContractData,
			ContractData: &xdr.ContractDataEntry{
				Contract:   ContractScAddress(contractID),
				Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
				Durability: xdr.ContractDataDurabilityPersistent,
				Val: xdr.ScVal{
					Type:     xdr.ScValTypeScvContractInstance,
					Instance: &xdr.ScContractInstance{Executable: exec},
				},
			},
		}
	}

	t.Run("wasm executable yields hash", func(t *testing.T) {
		data := makeInstance(xdr.ContractExecutable{
			Type:     xdr.ContractExecutableTypeContractExecutableWasm,
			WasmHash: &hash,
		})
		got := WasmHashFromInstance(data)
		require.NotNil(t, got)
		require.Equal(t, hash, *got)
	})

	t.Run("native SAC executable yields nil", func(t *testing.T) {
		data := makeInstance(xdr.ContractExecutable{
			Type: xdr.ContractExecutableTypeContractExecutableStellarAsset,
		})
		require.Nil(t, WasmHashFromInstance(data))
	})

	t.Run("non-instance entry yields nil", func(t *testing.T) {
		i128 := xdr.Int128Parts{Lo: 1}
		data := &xdr.LedgerEntryData{
			Type: xdr.LedgerEntryTypeContractData,
			ContractData: &xdr.ContractDataEntry{
				Contract: ContractScAddress(contractID),
				Val:      xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &i128},
			},
		}
		require.Nil(t, WasmHashFromInstance(data))
	})

	t.Run("nil yields nil", func(t *testing.T) {
		require.Nil(t, WasmHashFromInstance(nil))
	})
}
