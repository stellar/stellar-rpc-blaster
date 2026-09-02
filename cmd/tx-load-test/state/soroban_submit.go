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

// SetupResourcePadFactor adds a small safety margin to simulation-derived
// Soroban resources for one-off setup transactions. Standalone deployments can
// be slightly under-estimated for create-contract flows, and setup is not
// latency-sensitive enough for exact-minimum provisioning to matter.
const SetupResourcePadFactor = 1.50

const maxSorobanRestoreAttempts = 2

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
	for restoreAttempt := 0; restoreAttempt <= maxSorobanRestoreAttempts; restoreAttempt++ {
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
		if simResp.RestorePreamble != nil {
			if restoreAttempt == maxSorobanRestoreAttempts {
				return fmt.Errorf("simulate transaction still requires restore after %d attempts", restoreAttempt+1)
			}
			if err := submitRestorePreamble(ctx, logger, rpc, networkPassphrase, signer, simResp.RestorePreamble); err != nil {
				return fmt.Errorf("restore footprint: %w", err)
			}
			continue
		}

		var sorobanData xdr.SorobanTransactionData
		if err = xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); err != nil {
			return fmt.Errorf("parse simulation transaction data: %w", err)
		}
		if err := applySimulatedAuthEntries(op, simResp); err != nil {
			return fmt.Errorf("parse simulation auth entries: %w", err)
		}
		padSorobanResources(&sorobanData, SetupResourcePadFactor)
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

	return fmt.Errorf("soroban transaction restoration attempts exhausted")
}

// SubmitExtendFootprintTTLAndWait extends the TTL of the given ledger keys to
// extendTo ledgers from now via a single ExtendFootprintTtl transaction. The
// keys become the transaction's read-only footprint (ExtendFootprintTtl takes
// no read-write keys); unlike invokes, the simulator does not derive footprints
// for TTL ops, so the footprint is constructed here and the simulation supplies
// the resources and the rent-bearing resource fee. Entries in the footprint
// that are already live past extendTo are skipped by core at apply time.
func SubmitExtendFootprintTTLAndWait(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	signer *keypair.Full,
	keys []xdr.LedgerKey,
	extendTo uint32,
) error {
	if len(keys) == 0 {
		return nil
	}

	src, err := rpc.LoadAccount(ctx, signer.Address())
	if err != nil {
		return fmt.Errorf("load source account: %w", err)
	}
	seq, err := src.GetSequenceNumber()
	if err != nil {
		return fmt.Errorf("get source account sequence: %w", err)
	}

	op := buildExtendFootprintTTLOp(signer.Address(), keys, extendTo)
	sorobanData, err := simulateSorobanOp(ctx, rpc, signer.Address(), seq, op, "extend-ttl")
	if err != nil {
		return err
	}
	padSorobanResources(&sorobanData, SetupResourcePadFactor)
	op.Ext = xdr.TransactionExt{V: 1, SorobanData: &sorobanData}
	logger.WithFields(log.F{
		"extendTo":      extendTo,
		"readOnlyKeys":  len(sorobanData.Resources.Footprint.ReadOnly),
		"resourceFee":   sorobanData.ResourceFee,
		"diskReadBytes": sorobanData.Resources.DiskReadBytes,
	}).Info("extend-ttl simulation summary")
	return signAndSubmitSorobanOp(ctx, logger, rpc, networkPassphrase, signer, seq, op, int64(sorobanData.ResourceFee), "extend-ttl")
}

// SubmitRestoreFootprintAndWait restores the given archived ledger keys via a
// single RestoreFootprint transaction (the keys become the read-write
// footprint). Restored entries come back with the network minimum persistent
// TTL (~120 days on mainnet). Unlike the restore subcommand's probe flow --
// which on protocol 23 sees every archived entry as autorestore-class and
// submits nothing -- this restores any archived persistent entry directly,
// without depending on simulation preambles.
func SubmitRestoreFootprintAndWait(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	signer *keypair.Full,
	keys []xdr.LedgerKey,
) error {
	if len(keys) == 0 {
		return nil
	}

	src, err := rpc.LoadAccount(ctx, signer.Address())
	if err != nil {
		return fmt.Errorf("load source account: %w", err)
	}
	seq, err := src.GetSequenceNumber()
	if err != nil {
		return fmt.Errorf("get source account sequence: %w", err)
	}

	op := buildRestoreFootprintOp(signer.Address(), keys)
	sorobanData, err := simulateSorobanOp(ctx, rpc, signer.Address(), seq, op, "restore-footprint")
	if err != nil {
		return err
	}
	padSorobanResources(&sorobanData, SetupResourcePadFactor)
	op.Ext = xdr.TransactionExt{V: 1, SorobanData: &sorobanData}
	logger.WithFields(log.F{
		"readWriteKeys": len(sorobanData.Resources.Footprint.ReadWrite),
		"resourceFee":   sorobanData.ResourceFee,
		"writeBytes":    sorobanData.Resources.WriteBytes,
	}).Info("restore-footprint simulation summary")
	return signAndSubmitSorobanOp(ctx, logger, rpc, networkPassphrase, signer, seq, op, int64(sorobanData.ResourceFee), "restore-footprint")
}

// SimulateRestoreFootprintFee simulates (without submitting) a
// RestoreFootprint transaction over keys and returns the simulator's resource
// fee in stroops. Used by dry runs to approximate restore cost.
func SimulateRestoreFootprintFee(
	ctx context.Context,
	rpc *rpcclient.Client,
	sourceAddress string,
	sequence int64,
	keys []xdr.LedgerKey,
) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	sorobanData, err := simulateSorobanOp(ctx, rpc, sourceAddress, sequence, buildRestoreFootprintOp(sourceAddress, keys), "restore-footprint")
	if err != nil {
		return 0, err
	}
	return int64(sorobanData.ResourceFee), nil
}

func buildExtendFootprintTTLOp(sourceAddress string, keys []xdr.LedgerKey, extendTo uint32) *txnbuild.ExtendFootprintTtl {
	return &txnbuild.ExtendFootprintTtl{
		ExtendTo:      extendTo,
		SourceAccount: sourceAddress,
		Ext: xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{
			Resources: xdr.SorobanResources{
				Footprint: xdr.LedgerFootprint{ReadOnly: keys},
			},
		}},
	}
}

func buildRestoreFootprintOp(sourceAddress string, keys []xdr.LedgerKey) *txnbuild.RestoreFootprint {
	return &txnbuild.RestoreFootprint{
		SourceAccount: sourceAddress,
		Ext: xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{
			Resources: xdr.SorobanResources{
				Footprint: xdr.LedgerFootprint{ReadWrite: keys},
			},
		}},
	}
}

// signAndSubmitSorobanOp builds, signs, and submits the final transaction for
// an already-simulated Soroban op whose Ext carries the padded resources.
func signAndSubmitSorobanOp(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	signer *keypair.Full,
	sequence int64,
	op txnbuild.Operation,
	resourceFee int64,
	opName string,
) error {
	finalTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: signer.Address(), Sequence: sequence + 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              InclusionFee + resourceFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(TxTimeBoundSecs)},
	})
	if err != nil {
		return fmt.Errorf("build final %s transaction: %w", opName, err)
	}
	finalTx, err = finalTx.Sign(networkPassphrase, signer)
	if err != nil {
		return fmt.Errorf("sign %s transaction: %w", opName, err)
	}
	b64Final, err := finalTx.Base64()
	if err != nil {
		return fmt.Errorf("marshal %s transaction: %w", opName, err)
	}
	if SubmitAllAndPoll(ctx, logger, rpc, []string{b64Final}) > 0 {
		return fmt.Errorf("%s transaction failed", opName)
	}
	return nil
}

// SimulateExtendFootprintTTLFee simulates (without submitting) an
// ExtendFootprintTtl transaction over keys and returns the simulator's
// resource fee in stroops -- the rent-dominated cost the transaction would
// actually pay. Used by dry runs to approximate cost; no signature is needed
// because simulation ignores them.
func SimulateExtendFootprintTTLFee(
	ctx context.Context,
	rpc *rpcclient.Client,
	sourceAddress string,
	sequence int64,
	keys []xdr.LedgerKey,
	extendTo uint32,
) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	sorobanData, err := simulateSorobanOp(ctx, rpc, sourceAddress, sequence, buildExtendFootprintTTLOp(sourceAddress, keys, extendTo), "extend-ttl")
	if err != nil {
		return 0, err
	}
	return int64(sorobanData.ResourceFee), nil
}

// simulateSorobanOp simulates a single already-footprinted Soroban op (the
// op's Ext must carry the footprint -- unlike invokes, the simulator does not
// derive footprints for TTL/restore ops) and returns the simulator's
// SorobanTransactionData (resources + rent-bearing resource fee).
func simulateSorobanOp(
	ctx context.Context,
	rpc *rpcclient.Client,
	sourceAddress string,
	sequence int64,
	op txnbuild.Operation,
	opName string,
) (xdr.SorobanTransactionData, error) {
	simTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: sourceAddress, Sequence: sequence + 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              InclusionFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(TxTimeBoundSecs)},
	})
	if err != nil {
		return xdr.SorobanTransactionData{}, fmt.Errorf("build %s simulation transaction: %w", opName, err)
	}
	b64, err := simTx.Base64()
	if err != nil {
		return xdr.SorobanTransactionData{}, fmt.Errorf("encode %s simulation transaction: %w", opName, err)
	}

	simResp, err := rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return xdr.SorobanTransactionData{}, fmt.Errorf("simulate %s transaction: %w", opName, err)
	}
	if simResp.Error != "" {
		return xdr.SorobanTransactionData{}, fmt.Errorf("simulate %s transaction: %s", opName, simResp.Error)
	}

	var sorobanData xdr.SorobanTransactionData
	if err = xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); err != nil {
		return xdr.SorobanTransactionData{}, fmt.Errorf("parse %s simulation transaction data: %w", opName, err)
	}
	return sorobanData, nil
}

func submitRestorePreamble(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	signer *keypair.Full,
	preamble *protocol.RestorePreamble,
) error {
	if preamble == nil {
		return nil
	}
	sorobanData, err := restoreSorobanTransactionData(preamble)
	if err != nil {
		return err
	}
	logger.WithFields(log.F{
		"resourceFee":   sorobanData.ResourceFee,
		"readOnlyKeys":  len(sorobanData.Resources.Footprint.ReadOnly),
		"readWriteKeys": len(sorobanData.Resources.Footprint.ReadWrite),
	}).Info("soroban simulation requires archived-state restore")
	return SubmitAndWait(
		ctx,
		logger,
		rpc,
		networkPassphrase,
		signer,
		InclusionFee+int64(sorobanData.ResourceFee),
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
