package soroban

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

func TestSourceAccountContractAuthUsesInvokeArgs(t *testing.T) {
	invokeArgs := xdr.InvokeContractArgs{FunctionName: "transfer"}
	auth := SourceAccountContractAuth(invokeArgs)
	require.Len(t, auth, 1)
	require.Equal(t, xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount, auth[0].Credentials.Type)
	require.NotNil(t, auth[0].RootInvocation.Function.ContractFn)
	require.Equal(t, xdr.ScSymbol("transfer"), auth[0].RootInvocation.Function.ContractFn.FunctionName)
}
