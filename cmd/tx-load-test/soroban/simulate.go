package soroban

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const simulateInvokeTimeout = 30 * time.Second
const maxSimulationRestoreAttempts = 2

type SimulatedInvocation struct {
	Resources   xdr.SorobanResources
	ResourceFee xdr.Int64
	Footprint   xdr.LedgerFootprint
	AuthEntries []xdr.SorobanAuthorizationEntry
}

type RestoreProbeOptions struct {
	DryRun    bool
	PadFactor float64
}

type RestoreProbeResult struct {
	RestoreNeeded       bool
	RestoreTransactions int
	ReadOnlyKeys        int
	ReadWriteKeys       int
	ResourceFee         xdr.Int64
	Simulation          SimulatedInvocation
	HasSimulation       bool
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

	simResp, err := simulateInvokeContractResponse(ctx, st, txSourceKP, opSourceAddress, invokeArgs, baseFee)
	if err != nil {
		return SimulatedInvocation{}, err
	}

	sim, err := parseSimulatedInvocation(simResp)
	if err != nil {
		return SimulatedInvocation{}, err
	}
	return sim, nil
}

func simulateInvokeContractResponse(
	ctx context.Context,
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
	baseFee int64,
) (protocol.SimulateTransactionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, simulateInvokeTimeout)
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
		return protocol.SimulateTransactionResponse{}, fmt.Errorf("build simulation transaction: %w", err)
	}

	b64, err := tx.Base64()
	if err != nil {
		return protocol.SimulateTransactionResponse{}, fmt.Errorf("marshal simulation transaction: %w", err)
	}

	simResp, err := st.RPCClient.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return protocol.SimulateTransactionResponse{}, fmt.Errorf("simulate: %w", err)
	}
	if simResp.Error != "" {
		return protocol.SimulateTransactionResponse{}, fmt.Errorf("simulate: %s", simResp.Error)
	}
	return simResp, nil
}

func SimulatePaddedInvokeContract(
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
	baseFee int64,
	padFactor float64,
) (SimulatedInvocation, error) {
	ctx := context.Background()
	for restoreAttempt := 0; restoreAttempt <= maxSimulationRestoreAttempts; restoreAttempt++ {
		simResp, err := simulateInvokeContractResponse(ctx, st, txSourceKP, opSourceAddress, invokeArgs, baseFee)
		if err != nil {
			return SimulatedInvocation{}, err
		}
		if simResp.RestorePreamble != nil {
			if restoreAttempt == maxSimulationRestoreAttempts {
				return SimulatedInvocation{}, fmt.Errorf("simulate: restore still required after %d attempts", restoreAttempt+1)
			}
			if err := submitSimulationRestore(ctx, st, txSourceKP, simResp.RestorePreamble); err != nil {
				return SimulatedInvocation{}, fmt.Errorf("restore footprint: %w", err)
			}
			continue
		}

		sim, err := parseSimulatedInvocation(simResp)
		if err != nil {
			return SimulatedInvocation{}, err
		}
		PadSimulatedInvocation(&sim, padFactor)
		return sim, nil
	}

	return SimulatedInvocation{}, fmt.Errorf("simulate: restoration attempts exhausted")
}

func RestoreInvokeContract(
	ctx context.Context,
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
	baseFee int64,
	options RestoreProbeOptions,
) (RestoreProbeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var result RestoreProbeResult
	for restoreAttempt := 0; restoreAttempt <= maxSimulationRestoreAttempts; restoreAttempt++ {
		simResp, err := simulateInvokeContractResponse(ctx, st, txSourceKP, opSourceAddress, invokeArgs, baseFee)
		if err != nil {
			return result, err
		}
		if simResp.RestorePreamble == nil {
			sim, err := parseSimulatedInvocation(simResp)
			if err != nil {
				return result, err
			}
			PadSimulatedInvocation(&sim, options.PadFactor)
			result.Simulation = sim
			result.HasSimulation = true
			return result, nil
		}

		result.RestoreNeeded = true
		sorobanData, err := restoreSorobanTransactionData(simResp.RestorePreamble)
		if err != nil {
			return result, err
		}
		result.ReadOnlyKeys += len(sorobanData.Resources.Footprint.ReadOnly)
		result.ReadWriteKeys += len(sorobanData.Resources.Footprint.ReadWrite)
		result.ResourceFee += sorobanData.ResourceFee

		if options.DryRun {
			return result, nil
		}
		if restoreAttempt == maxSimulationRestoreAttempts {
			return result, fmt.Errorf("simulate: restore still required after %d attempts", restoreAttempt+1)
		}
		if err := submitSimulationRestore(ctx, st, txSourceKP, simResp.RestorePreamble); err != nil {
			return result, fmt.Errorf("restore footprint: %w", err)
		}
		result.RestoreTransactions++
	}

	return result, fmt.Errorf("simulate: restoration attempts exhausted")
}

func submitSimulationRestore(ctx context.Context, st *state.State, fallbackSigner *keypair.Full, preamble *protocol.RestorePreamble) error {
	sorobanData, err := restoreSorobanTransactionData(preamble)
	if err != nil {
		return err
	}
	signer := st.FeePayerKP
	if signer == nil {
		signer = fallbackSigner
	}
	if signer == nil {
		return fmt.Errorf("missing signer for restore transaction")
	}
	if st.NetworkPassphrase == "" {
		return fmt.Errorf("missing network passphrase for restore transaction")
	}
	ctx, cancel := context.WithTimeout(ctx, simulateInvokeTimeout)
	defer cancel()
	logger := log.New().WithField("phase", "benchmark restore")
	return state.SubmitAndWait(
		ctx,
		logger,
		st.RPCClient,
		st.NetworkPassphrase,
		signer,
		state.InclusionFee+int64(sorobanData.ResourceFee),
		[]txnbuild.Operation{&txnbuild.RestoreFootprint{
			SourceAccount: signer.Address(),
			Ext:           xdr.TransactionExt{V: 1, SorobanData: &sorobanData},
		}},
	)
}

func restoreSorobanTransactionData(preamble *protocol.RestorePreamble) (xdr.SorobanTransactionData, error) {
	if preamble == nil {
		return xdr.SorobanTransactionData{}, fmt.Errorf("missing restore preamble")
	}
	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(preamble.TransactionDataXDR, &sorobanData); err != nil {
		return xdr.SorobanTransactionData{}, fmt.Errorf("parse restore transaction data: %w", err)
	}
	if preamble.MinResourceFee > 0 && sorobanData.ResourceFee < xdr.Int64(preamble.MinResourceFee) {
		sorobanData.ResourceFee = xdr.Int64(preamble.MinResourceFee)
	}
	return sorobanData, nil
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
