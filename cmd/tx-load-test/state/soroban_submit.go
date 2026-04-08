package state

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// setupResourcePadFactor adds a small safety margin to simulation-derived
// Soroban resources for one-off setup transactions. Standalone deployments can
// be slightly under-estimated for create-contract flows, and setup is not
// latency-sensitive enough for exact-minimum provisioning to matter.
const setupResourcePadFactor = 1.10

// SubmitSorobanAndWait builds a Soroban transaction containing a single
// InvokeHostFunction operation, simulates it to obtain the accurate resource
// footprint and fees, then signs with signer and submits it.
func SubmitSorobanAndWait(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	signer *keypair.Full,
	op *txnbuild.InvokeHostFunction,
) error {
	src, err := rpc.LoadAccount(ctx, signer.Address())
	if err != nil {
		return fmt.Errorf("load source account: %w", err)
	}
	seq, err := src.GetSequenceNumber()
	if err != nil {
		return fmt.Errorf("get source account sequence: %w", err)
	}

	simOp := *op
	simOp.Auth = nil
	simOp.Ext = xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}}

	simTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: signer.Address(), Sequence: seq + 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&simOp},
		BaseFee:              InclusionFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(TxTimeBoundSecs)},
	})
	if err != nil {
		return fmt.Errorf("build soroban simulation transaction: %w", err)
	}

	b64, err := simTx.Base64()
	if err != nil {
		return fmt.Errorf("encode simulation transaction: %w", err)
	}

	simResp, err := rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{
		Transaction: b64,
	})
	if err != nil {
		return fmt.Errorf("simulate transaction: %w", err)
	}
	if simResp.Error != "" {
		return fmt.Errorf("simulate transaction: %s", simResp.Error)
	}

	var sorobanData xdr.SorobanTransactionData
	if err = xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); err != nil {
		return fmt.Errorf("parse simulation transaction data: %w", err)
	}
	if err := applySimulatedAuthEntries(op, simResp); err != nil {
		return fmt.Errorf("parse simulation auth entries: %w", err)
	}
	padSorobanResources(&sorobanData, setupResourcePadFactor)
	op.Ext = xdr.TransactionExt{V: 1, SorobanData: &sorobanData}
	logSorobanSimulation(logger, op, simResp, sorobanData)

	finalTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: signer.Address(), Sequence: seq + 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              InclusionFee + int64(sorobanData.ResourceFee),
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(TxTimeBoundSecs)},
	})
	if err != nil {
		return fmt.Errorf("build final soroban transaction: %w", err)
	}

	finalTx, err = finalTx.Sign(networkPassphrase, signer)
	if err != nil {
		return fmt.Errorf("sign soroban transaction: %w", err)
	}

	b64Final, err := finalTx.Base64()
	if err != nil {
		return fmt.Errorf("marshal soroban transaction: %w", err)
	}
	if SubmitAllAndPoll(ctx, logger, rpc, []string{b64Final}) > 0 {
		return fmt.Errorf("soroban transaction failed")
	}
	return nil
}

func padSorobanResources(data *xdr.SorobanTransactionData, factor float64) {
	if data == nil || factor <= 1 {
		return
	}
	data.Resources.Instructions = xdr.Uint32(float64(data.Resources.Instructions) * factor)
	data.Resources.DiskReadBytes = xdr.Uint32(float64(data.Resources.DiskReadBytes) * factor)
	data.Resources.WriteBytes = xdr.Uint32(float64(data.Resources.WriteBytes) * factor)
	data.ResourceFee = xdr.Int64(float64(data.ResourceFee) * factor)
}

func applySimulatedAuthEntries(op *txnbuild.InvokeHostFunction, simResp protocol.SimulateTransactionResponse) error {
	if op == nil || len(simResp.Results) == 0 || simResp.Results[0].AuthXDR == nil {
		return nil
	}

	authXDR := *simResp.Results[0].AuthXDR
	authEntries := make([]xdr.SorobanAuthorizationEntry, len(authXDR))
	for i, encoded := range authXDR {
		if err := xdr.SafeUnmarshalBase64(encoded, &authEntries[i]); err != nil {
			return fmt.Errorf("auth[%d]: %w", i, err)
		}
	}
	op.Auth = authEntries
	return nil
}

// logSorobanSimulation emits a compact summary of a simulated Soroban
// transaction so send-time failures can be correlated with the simulated
// resource budget and footprint.
func logSorobanSimulation(
	logger *log.Entry,
	op *txnbuild.InvokeHostFunction,
	simResp protocol.SimulateTransactionResponse,
	sorobanData xdr.SorobanTransactionData,
) {
	if op == nil {
		return
	}
	entry := logger.WithFields(log.F{
		"resourceFee":      sorobanData.ResourceFee,
		"minResourceFee":   simResp.MinResourceFee,
		"readOnlyKeys":     len(sorobanData.Resources.Footprint.ReadOnly),
		"readWriteKeys":    len(sorobanData.Resources.Footprint.ReadWrite),
		"instructions":     sorobanData.Resources.Instructions,
		"diskReadBytes":    sorobanData.Resources.DiskReadBytes,
		"writeBytes":       sorobanData.Resources.WriteBytes,
		"authEntries":      len(op.Auth),
		"simResultCount":   len(simResp.Results),
		"simEventCount":    len(simResp.EventsXDR),
		"stateChangeCount": len(simResp.StateChanges),
	})
	if invoke := op.HostFunction.InvokeContract; invoke != nil {
		entry = entry.WithField("contractFn", invoke.FunctionName)
	}
	if simResp.RestorePreamble != nil {
		entry = entry.WithField("restoreRequired", true)
	}
	entry.Info("soroban simulation summary")

	for i, ev := range simResp.EventsXDR {
		entry.WithField("event", i).Debugf("simulation event: %s", ev)
	}
	for i, change := range simResp.StateChanges {
		entry.WithFields(log.F{
			"change": i,
			"type":   change.Type.String(),
		}).Debug("simulation state change")
	}
}
