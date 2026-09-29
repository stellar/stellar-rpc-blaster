package benchmark

import (
	"encoding/json"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

func TestBuildSorobanSendTransactionBodyBuildsSignedJSONRPCRequest(t *testing.T) {
	txSource, err := keypair.Random()
	require.NoError(t, err)
	coSigner, err := keypair.Random()
	require.NoError(t, err)
	contractID := xdr.ContractId{}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID},
		FunctionName:    "transfer",
	}
	resourceFee := xdr.Int64(321)

	body, err := buildSorobanSendTransactionBody(sorobanSendTransactionParams{
		RPCID:             99,
		NetworkPassphrase: "Test SDF Network ; September 2015",
		TxSource:          txSource,
		Sequence:          41,
		Signers:           []*keypair.Full{txSource, coSigner},
		OpSourceAccount:   coSigner.Address(),
		InvokeArgs:        invokeArgs,
		AuthEntries: []xdr.SorobanAuthorizationEntry{{
			Credentials: xdr.SorobanCredentials{Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount},
			RootInvocation: xdr.SorobanAuthorizedInvocation{
				Function: xdr.SorobanAuthorizedFunction{
					Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
					ContractFn: &invokeArgs,
				},
			},
		}},
		Resources: xdr.SorobanResources{
			Instructions:  11,
			DiskReadBytes: 22,
			WriteBytes:    33,
		},
		ResourceFee: resourceFee,
	})
	require.NoError(t, err)

	var request rpcJSONBody
	require.NoError(t, json.Unmarshal(body, &request))
	require.Equal(t, int64(99), request.ID)
	require.Equal(t, "2.0", request.JSONRPC)
	require.Equal(t, protocol.SendTransactionMethodName, request.Method)
	require.NotEmpty(t, request.Params["transaction"])

	gtx, err := txnbuild.TransactionFromXDR(request.Params["transaction"])
	require.NoError(t, err)
	tx, ok := gtx.Transaction()
	require.True(t, ok)
	require.Equal(t, txSource.Address(), tx.SourceAccount().AccountID)
	require.Equal(t, int64(41), tx.SequenceNumber())
	require.Equal(t, benchmarkBaseFee+int64(resourceFee), tx.BaseFee())
	require.Len(t, tx.Signatures(), 2)
	operations := tx.Operations()
	require.Len(t, operations, 1)
	op, ok := operations[0].(*txnbuild.InvokeHostFunction)
	require.True(t, ok)
	require.Equal(t, coSigner.Address(), op.SourceAccount)
	require.Len(t, op.Auth, 1)
	require.NotNil(t, op.Ext.SorobanData)
	require.Equal(t, resourceFee, op.Ext.SorobanData.ResourceFee)
	require.Equal(t, xdr.ScSymbol("transfer"), op.HostFunction.InvokeContract.FunctionName)
}

func TestPopulateJSONRPCTargetSetsRequestFields(t *testing.T) {
	target := &vegeta.Target{}
	body := []byte(`{"ok":true}`)
	populateJSONRPCTarget(target, "https://rpc.example", body)
	require.Equal(t, "POST", target.Method)
	require.Equal(t, "https://rpc.example", target.URL)
	require.Equal(t, body, target.Body)
	require.Equal(t, "application/json", target.Header.Get("Content-Type"))
}
