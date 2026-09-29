package soroban

import (
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestParseSimulatedInvocationDecodesResourcesAndAuth(t *testing.T) {
	contractID := xdr.ContractId{1}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		},
		FunctionName: "noop",
	}
	entry := xdr.SorobanAuthorizationEntry{
		Credentials: xdr.SorobanCredentials{Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount},
		RootInvocation: xdr.SorobanAuthorizedInvocation{
			Function: xdr.SorobanAuthorizedFunction{
				Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
				ContractFn: &invokeArgs,
			},
		},
	}
	encodedAuth, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)
	accountID, err := xdr.AddressToAccountId("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
	require.NoError(t, err)
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Footprint: xdr.LedgerFootprint{
				ReadOnly: []xdr.LedgerKey{{
					Type:    xdr.LedgerEntryTypeAccount,
					Account: &xdr.LedgerKeyAccount{AccountId: accountID},
				}},
			},
			Instructions:  10,
			DiskReadBytes: 20,
			WriteBytes:    30,
		},
		ResourceFee: 40,
	}
	encodedData, err := xdr.MarshalBase64(data)
	require.NoError(t, err)

	sim, err := parseSimulatedInvocation(protocol.SimulateTransactionResponse{
		TransactionDataXDR: encodedData,
		Results:            []protocol.SimulateHostFunctionResult{{AuthXDR: &[]string{encodedAuth}}},
	})
	require.NoError(t, err)
	require.Equal(t, data.Resources, sim.Resources)
	require.Equal(t, data.ResourceFee, sim.ResourceFee)
	require.Equal(t, data.Resources.Footprint, sim.Footprint)
	require.Len(t, sim.AuthEntries, 1)
	require.Equal(t, entry.Credentials.Type, sim.AuthEntries[0].Credentials.Type)
}

func TestPadSimulatedInvocation(t *testing.T) {
	sim := SimulatedInvocation{
		Resources: xdr.SorobanResources{
			Instructions:  100,
			DiskReadBytes: 200,
			WriteBytes:    300,
		},
		ResourceFee: 400,
	}

	PadSimulatedInvocation(&sim, 1.10)

	require.Equal(t, xdr.Uint32(110), sim.Resources.Instructions)
	require.Equal(t, xdr.Uint32(220), sim.Resources.DiskReadBytes)
	require.Equal(t, xdr.Uint32(330), sim.Resources.WriteBytes)
	require.Equal(t, xdr.Int64(440), sim.ResourceFee)

	PadSimulatedInvocation(&sim, 1)
	require.Equal(t, xdr.Uint32(110), sim.Resources.Instructions)
}
