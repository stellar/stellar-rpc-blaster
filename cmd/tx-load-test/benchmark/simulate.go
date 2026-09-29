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

type simulatedInvocation struct {
	resources   xdr.SorobanResources
	resourceFee xdr.Int64
	footprint   xdr.LedgerFootprint
	authEntries []xdr.SorobanAuthorizationEntry
}

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
	sim, err := simulateInvokeContractDetailed(st, txSourceKP, opSourceAddress, invokeArgs)
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, err
	}
	return sim.resources, sim.resourceFee, sim.footprint, nil
}

func simulateInvokeContractDetailed(
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
) (simulatedInvocation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	op := txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &invokeArgs,
		},
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
		return simulatedInvocation{}, fmt.Errorf("build simulation transaction: %w", err)
	}

	b64, err := tx.Base64()
	if err != nil {
		return simulatedInvocation{}, fmt.Errorf("marshal simulation transaction: %w", err)
	}

	simResp, err := st.RPCClient.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return simulatedInvocation{}, fmt.Errorf("simulate: %w", err)
	}
	if simResp.Error != "" {
		return simulatedInvocation{}, fmt.Errorf("simulate: %s", simResp.Error)
	}

	var sorobanData xdr.SorobanTransactionData
	if err = xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); err != nil {
		return simulatedInvocation{}, fmt.Errorf("parse simulation result: %w", err)
	}

	var authEntries []xdr.SorobanAuthorizationEntry
	if len(simResp.Results) > 0 && simResp.Results[0].AuthXDR != nil {
		authEntries = make([]xdr.SorobanAuthorizationEntry, len(*simResp.Results[0].AuthXDR))
		for i, encoded := range *simResp.Results[0].AuthXDR {
			if err := xdr.SafeUnmarshalBase64(encoded, &authEntries[i]); err != nil {
				return simulatedInvocation{}, fmt.Errorf("parse simulation auth[%d]: %w", i, err)
			}
		}
	}

	return simulatedInvocation{
		resources:   sorobanData.Resources,
		resourceFee: sorobanData.ResourceFee,
		footprint:   sorobanData.Resources.Footprint,
		authEntries: authEntries,
	}, nil
}
