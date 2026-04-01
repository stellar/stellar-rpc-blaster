package benchmark

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func sourceAccountContractAuth(invokeArgs xdr.InvokeContractArgs) []xdr.SorobanAuthorizationEntry {
	return []xdr.SorobanAuthorizationEntry{{
		Credentials: xdr.SorobanCredentials{
			Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
		},
		RootInvocation: xdr.SorobanAuthorizedInvocation{
			Function: xdr.SorobanAuthorizedFunction{
				Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
				ContractFn: &invokeArgs,
			},
		},
	}}
}

func simulateInvokeContract(
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
) (xdr.SorobanResources, xdr.Int64, xdr.LedgerFootprint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	op := txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &invokeArgs,
		},
		Auth:          sourceAccountContractAuth(invokeArgs),
		SourceAccount: opSourceAddress,
		Ext:           xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: txSourceKP.Address(), Sequence: 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              benchmarkBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
	})
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("build simulation transaction: %w", err)
	}

	b64, err := tx.Base64()
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("marshal simulation transaction: %w", err)
	}

	simResp, err := st.RPCClient.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("simulate: %w", err)
	}
	if simResp.Error != "" {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("simulate: %s", simResp.Error)
	}

	var sorobanData xdr.SorobanTransactionData
	if err = xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("parse simulation result: %w", err)
	}

	return sorobanData.Resources, sorobanData.ResourceFee, sorobanData.Resources.Footprint, nil
}
