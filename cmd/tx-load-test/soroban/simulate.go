package soroban

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

const simulateInvokeTimeout = 30 * time.Second

type SimulatedInvocation struct {
	Resources   xdr.SorobanResources
	ResourceFee xdr.Int64
	Footprint   xdr.LedgerFootprint
	AuthEntries []xdr.SorobanAuthorizationEntry
}

func SimulateInvokeContract(
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
	baseFee int64,
) (SimulatedInvocation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simulateInvokeTimeout)
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
		BaseFee:              baseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
	})
	if err != nil {
		return SimulatedInvocation{}, fmt.Errorf("build simulation transaction: %w", err)
	}

	b64, err := tx.Base64()
	if err != nil {
		return SimulatedInvocation{}, fmt.Errorf("marshal simulation transaction: %w", err)
	}

	simResp, err := st.RPCClient.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return SimulatedInvocation{}, fmt.Errorf("simulate: %w", err)
	}
	if simResp.Error != "" {
		return SimulatedInvocation{}, fmt.Errorf("simulate: %s", simResp.Error)
	}

	sim, err := parseSimulatedInvocation(simResp)
	if err != nil {
		return SimulatedInvocation{}, err
	}
	return sim, nil
}

func SimulatePaddedInvokeContract(
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
	baseFee int64,
	padFactor float64,
) (SimulatedInvocation, error) {
	sim, err := SimulateInvokeContract(st, txSourceKP, opSourceAddress, invokeArgs, baseFee)
	if err != nil {
		return SimulatedInvocation{}, err
	}
	PadSimulatedInvocation(&sim, padFactor)
	return sim, nil
}

func parseSimulatedInvocation(simResp protocol.SimulateTransactionResponse) (SimulatedInvocation, error) {
	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); err != nil {
		return SimulatedInvocation{}, fmt.Errorf("parse simulation result: %w", err)
	}

	var authEntries []xdr.SorobanAuthorizationEntry
	if len(simResp.Results) > 0 && simResp.Results[0].AuthXDR != nil {
		authEntries = make([]xdr.SorobanAuthorizationEntry, len(*simResp.Results[0].AuthXDR))
		for i, encoded := range *simResp.Results[0].AuthXDR {
			if err := xdr.SafeUnmarshalBase64(encoded, &authEntries[i]); err != nil {
				return SimulatedInvocation{}, fmt.Errorf("parse simulation auth[%d]: %w", i, err)
			}
		}
	}

	return SimulatedInvocation{
		Resources:   sorobanData.Resources,
		ResourceFee: sorobanData.ResourceFee,
		Footprint:   sorobanData.Resources.Footprint,
		AuthEntries: authEntries,
	}, nil
}

func PadSimulatedInvocation(sim *SimulatedInvocation, factor float64) {
	if sim == nil || factor <= 1 {
		return
	}
	sim.Resources.Instructions = xdr.Uint32(float64(sim.Resources.Instructions) * factor)
	sim.Resources.DiskReadBytes = xdr.Uint32(float64(sim.Resources.DiskReadBytes) * factor)
	sim.Resources.WriteBytes = xdr.Uint32(float64(sim.Resources.WriteBytes) * factor)
	sim.ResourceFee = xdr.Int64(float64(sim.ResourceFee) * factor)
}