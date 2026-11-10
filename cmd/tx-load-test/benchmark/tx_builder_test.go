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
	feePayer, err := keypair.Random()
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
		FeePayerKP:        feePayer,
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
	feeBump, ok := gtx.FeeBump()
	require.True(t, ok)
	require.Equal(t, feePayer.Address(), feeBump.FeeAccount())
	// BaseFee is randomly sampled from [benchmarkBaseFeeMin, benchmarkBaseFeeMax]
	// per submission, so assert against the range rather than a fixed value.
	require.GreaterOrEqual(t, feeBump.MaxFee(), benchmarkBaseFeeMin*2+int64(resourceFee))
	require.LessOrEqual(t, feeBump.MaxFee(), benchmarkBaseFeeMax*2+int64(resourceFee))
	require.Len(t, feeBump.Signatures(), 1)

	tx := feeBump.InnerTransaction()
	require.Equal(t, txSource.Address(), tx.SourceAccount().AccountID)
	require.Equal(t, int64(41), tx.SequenceNumber())
	require.GreaterOrEqual(t, tx.BaseFee(), benchmarkBaseFeeMin+int64(resourceFee))
	require.LessOrEqual(t, tx.BaseFee(), benchmarkBaseFeeMax+int64(resourceFee))
	// feeBump.MaxFee covers (innerOps+1) ops at the inner per-op rate plus the
	// resource fee once: feeBump.MaxFee == 2*tx.BaseFee - resourceFee, since
	// txnbuild reports tx.BaseFee as (per-op inclusion fee + resource fee).
	require.Equal(t, feeBump.MaxFee(), tx.BaseFee()*2-int64(resourceFee))
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

func TestBuildSorobanSendTransactionBodyCarriesAutorestoreExt(t *testing.T) {
	// Protocol-23+ autorestore signal: simulator returns
	// SorobanTransactionDataExt.V1 with the indices of footprint.readWrite
	// entries that core must auto-restore inline. Without this on the
	// submitted tx apply fails with invokeHostFunctionEntryArchived even
	// though the inflated resource fee already prices restoration.
	txSource, err := keypair.Random()
	require.NoError(t, err)
	feePayer, err := keypair.Random()
	require.NoError(t, err)
	contractID := xdr.ContractId{}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID},
		FunctionName:    "transfer",
	}
	resourceFee := xdr.Int64(110_000_000)
	dataExt := xdr.SorobanTransactionDataExt{
		V: 1,
		ResourceExt: &xdr.SorobanResourcesExtV0{
			ArchivedSorobanEntries: []xdr.Uint32{0, 3},
		},
	}

	body, err := buildSorobanSendTransactionBody(sorobanSendTransactionParams{
		RPCID:             7,
		NetworkPassphrase: "Test SDF Network ; September 2015",
		FeePayerKP:        feePayer,
		TxSource:          txSource,
		Sequence:          1,
		Signers:           []*keypair.Full{txSource},
		OpSourceAccount:   txSource.Address(),
		InvokeArgs:        invokeArgs,
		Resources:         xdr.SorobanResources{Instructions: 1},
		ResourceFee:       resourceFee,
		SorobanDataExt:    dataExt,
	})
	require.NoError(t, err)

	var request rpcJSONBody
	require.NoError(t, json.Unmarshal(body, &request))
	gtx, err := txnbuild.TransactionFromXDR(request.Params["transaction"])
	require.NoError(t, err)
	feeBump, ok := gtx.FeeBump()
	require.True(t, ok)

	op, ok := feeBump.InnerTransaction().Operations()[0].(*txnbuild.InvokeHostFunction)
	require.True(t, ok)
	require.NotNil(t, op.Ext.SorobanData)
	require.Equal(t, int32(1), int32(op.Ext.SorobanData.Ext.V))
	require.NotNil(t, op.Ext.SorobanData.Ext.ResourceExt)
	require.Equal(t,
		[]xdr.Uint32{0, 3},
		op.Ext.SorobanData.Ext.ResourceExt.ArchivedSorobanEntries,
	)

	// When the simulator's resource fee crosses heavyResourceFeeThreshold,
	// benchmarkInnerBaseFee must lift the per-op inclusion fee floor so that
	// the fee-bump's totalFee covers the inflated inner total. With a
	// resource fee of 1.1e8 stroops a 10k-100k inclusion sample would leave
	// the bump 8-10x short of clearing core's surge floor.
	require.GreaterOrEqual(t, feeBump.InnerTransaction().BaseFee(), int64(1_000_000)+int64(resourceFee))
}

func TestBenchmarkInnerBaseFeeBoostsFloorForHeavyResourceFee(t *testing.T) {
	for i := 0; i < 64; i++ {
		got := benchmarkInnerBaseFee(20_000_000)
		require.GreaterOrEqual(t, got, int64(1_000_000),
			"heavy-resource sample %d=%d should not undercut the 1M floor", i, got)
	}
	for i := 0; i < 64; i++ {
		got := benchmarkInnerBaseFee(1_000)
		require.GreaterOrEqual(t, got, benchmarkBaseFeeMin)
		require.LessOrEqual(t, got, benchmarkBaseFeeMax,
			"light-resource sample %d=%d should stay within the normal sample range", i, got)
	}
}

func TestPopulateJSONRPCTargetSetsRequestFields(t *testing.T) {
	target := &vegeta.Target{}
	body := []byte(`{"ok":true}`)
	populateJSONRPCTarget(target, "https://rpc.example", body, 0)
	require.Equal(t, "POST", target.Method)
	require.Equal(t, "https://rpc.example", target.URL)
	require.Equal(t, body, target.Body)
	require.Equal(t, "application/json", target.Header.Get("Content-Type"))
}
