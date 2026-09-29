package state

import (
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestApplySimulatedAuthEntriesReplacesOperationAuth(t *testing.T) {
	contractID := xdr.ContractId{1}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		},
		FunctionName: "noop",
	}
	entry := xdr.SorobanAuthorizationEntry{
		Credentials: xdr.SorobanCredentials{
			Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
		},
		RootInvocation: xdr.SorobanAuthorizedInvocation{
			Function: xdr.SorobanAuthorizedFunction{
				Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
				ContractFn: &invokeArgs,
			},
		},
	}
	encoded, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)

	op := &txnbuild.InvokeHostFunction{}
	err = applySimulatedAuthEntries(op, protocol.SimulateTransactionResponse{
		Results: []protocol.SimulateHostFunctionResult{{AuthXDR: &[]string{encoded}}},
	})
	require.NoError(t, err)
	require.Len(t, op.Auth, 1)
	require.Equal(t, entry.RootInvocation.Function.Type, op.Auth[0].RootInvocation.Function.Type)
	require.NotNil(t, op.Auth[0].RootInvocation.Function.ContractFn)
	require.Equal(t, invokeArgs.FunctionName, op.Auth[0].RootInvocation.Function.ContractFn.FunctionName)
}

func TestPadSorobanResources(t *testing.T) {
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Instructions:  100,
			DiskReadBytes: 200,
			WriteBytes:    300,
		},
		ResourceFee: 400,
	}

	padSorobanResources(&data, 1.10)

	require.Equal(t, xdr.Uint32(110), data.Resources.Instructions)
	require.Equal(t, xdr.Uint32(220), data.Resources.DiskReadBytes)
	require.Equal(t, xdr.Uint32(330), data.Resources.WriteBytes)
	require.Equal(t, xdr.Int64(440), data.ResourceFee)

	padSorobanResources(&data, 1)
	require.Equal(t, xdr.Uint32(110), data.Resources.Instructions)
}
