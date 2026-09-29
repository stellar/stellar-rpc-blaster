package state

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// InclusionFee is the per-operation base fee (in stroops) applied to every
// transaction we submit. 200 stroops gives comfortable headroom above the
// network minimum (100 stroops) without being wasteful.
const InclusionFee = 200

// TxTimeBoundSecs is the MaxTime window (in seconds from now) set on every
// setup transaction. Stellar closes a ledger every ~5 s, so 300 s (60 ledgers)
// is ample under normal network conditions. Transactions submitted outside
// this window are rejected with TxTooLate, preventing stale txs from being
// included long after we stopped waiting for them.
const TxTimeBoundSecs = 300

// txPollTimeout is the maximum time SubmitAllAndPoll will block waiting for
// all submitted transactions to reach a terminal on-chain state. Under normal
// conditions confirmation takes a few ledgers (~30 s); 2 minutes gives ample
// headroom while bounding the hang window.
const txPollTimeout = 2 * time.Minute

// DeriveKeypair deterministically derives a keypair from base by overwriting
// the last 4 bytes of its raw seed with the big-endian representation of index.
// Callers must ensure index > 0 so the derived keypair differs from the base.
func DeriveKeypair(base *keypair.Full, index int) (*keypair.Full, error) {
	rawSeed, err := strkey.Decode(strkey.VersionByteSeed, base.Seed())
	if err != nil {
		return nil, err
	}
	var derived [32]byte
	copy(derived[:], rawSeed)
	binary.BigEndian.PutUint32(derived[28:], uint32(index))
	return keypair.FromRawSeed(derived)
}

// SubmitAndWait builds a classic transaction from ops sourced by signer,
// signs, submits, and polls until terminal state. baseFee is the per-operation
// fee in stroops; pass InclusionFee for normal serial transactions or a higher
// value when many large transactions will be submitted concurrently (to
// survive surge-pricing rejection).
func SubmitAndWait(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	signer *keypair.Full,
	baseFee int64,
	ops []txnbuild.Operation,
) error {
	src, err := rpc.LoadAccount(ctx, signer.Address())
	if err != nil {
		return fmt.Errorf("load source account: %w", err)
	}
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        src,
		IncrementSequenceNum: true,
		Operations:           ops,
		BaseFee:              baseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(TxTimeBoundSecs)},
	})
	if err != nil {
		return fmt.Errorf("build transaction: %w", err)
	}
	tx, err = tx.Sign(networkPassphrase, signer)
	if err != nil {
		return fmt.Errorf("sign transaction: %w", err)
	}
	b64, err := tx.Base64()
	if err != nil {
		return fmt.Errorf("marshal transaction: %w", err)
	}
	if SubmitAllAndPoll(ctx, logger, rpc, []string{b64}) > 0 {
		return fmt.Errorf("transaction failed")
	}
	return nil
}

// SubmitFeeBumpAndWait wraps an already-signed inner transaction in a fee-bump
// signed by feePayerKP, submits it, and polls until terminal state. The inner
// transaction must already be signed by its source account before being passed
// here.
//
// If the RPC node rejects the fee-bump with TxInsufficientFee (surge pricing),
// the function escalates the per-op fee up to 2* InclusionFee and retries
// until the transaction is accepted or ctx is cancelled.
func SubmitFeeBumpAndWait(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	feePayerKP *keypair.Full,
	innerTx *txnbuild.Transaction,
) error {
	baseFee := int64(InclusionFee)
	maxFee := baseFee * 2

	for {
		fb, err := txnbuild.NewFeeBumpTransaction(txnbuild.FeeBumpTransactionParams{
			Inner:      innerTx,
			FeeAccount: feePayerKP.Address(),
			BaseFee:    baseFee,
		})
		if err != nil {
			return fmt.Errorf("build fee-bump: %w", err)
		}
		fb, err = fb.Sign(networkPassphrase, feePayerKP)
		if err != nil {
			return fmt.Errorf("sign fee-bump: %w", err)
		}
		b64, err := fb.Base64()
		if err != nil {
			return fmt.Errorf("marshal fee-bump: %w", err)
		}

		resp, err := rpc.SendTransaction(ctx, protocol.SendTransactionRequest{Transaction: b64})
		if err != nil {
			return fmt.Errorf("send fee-bump: %w", err)
		}
		if resp.ErrorResultXDR != "" {
			if isInsufficientFee(resp.ErrorResultXDR) {
				if baseFee < maxFee {
					baseFee = min(baseFee*2, maxFee)
					logger.Warnf("surge pricing  -- escalating to %d stroops/op, retrying", baseFee)
				} else {
					logger.Warn("surge pricing  -- retrying at max fee")
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("context cancelled while retrying surge pricing: %w", ctx.Err())
				case <-time.After(time.Second):
				}
				continue
			}
			logSendTransactionRejection(logger, resp)
			code := decodeResultCode(resp.ErrorResultXDR)
			return fmt.Errorf("fee-bump rejected: resultCode=%s", code)
		}

		// Accepted  -- poll for on-chain confirmation.
		logger.Debugf("submitted hash=%s (fee=%d stroops/op)", resp.Hash, baseFee)
		pollCtx, pollCancel := context.WithTimeout(ctx, txPollTimeout)
		result, err := rpc.PollTransaction(pollCtx, resp.Hash)
		pollCancel()
		if err != nil {
			return fmt.Errorf("poll fee-bump hash=%s: %w", resp.Hash, err)
		}
		if result.Status != protocol.TransactionStatusSuccess {
			logTxFailure(logger, resp.Hash, result.ResultXDR, result.DiagnosticEventsXDR)
			return fmt.Errorf("fee-bump transaction failed on-chain")
		}
		logger.Infof("confirmed hash=%s", resp.Hash)
		return nil
	}
}

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

	// Provide a minimal placeholder SorobanTransactionData so the envelope is
	// recognized as a Soroban (v1) transaction by the RPC simulator.
	op.Ext = xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        src,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              InclusionFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(TxTimeBoundSecs)},
	})
	if err != nil {
		return fmt.Errorf("build soroban transaction: %w", err)
	}

	b64, err := tx.Base64()
	if err != nil {
		return fmt.Errorf("encode transaction: %w", err)
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
	logSorobanSimulation(logger, op, simResp, sorobanData)

	// Reconstruct the envelope with the accurate Soroban data and total fee.
	xdrEnv := tx.ToXDR()
	xdrEnv.V1.Tx.Ext = xdr.TransactionExt{V: 1, SorobanData: &sorobanData}
	xdrEnv.V1.Tx.Fee = xdr.Uint32(int64(xdrEnv.V1.Tx.Fee) + simResp.MinResourceFee)

	envelopeBytes, err := xdrEnv.MarshalBinary()
	if err != nil {
		return fmt.Errorf("re-marshal updated transaction: %w", err)
	}

	b64Updated := base64.StdEncoding.EncodeToString(envelopeBytes)

	gt, err := txnbuild.TransactionFromXDR(b64Updated)
	if err != nil {
		return fmt.Errorf("parse updated transaction: %w", err)
	}
	updatedTx, ok := gt.Transaction()
	if !ok {
		return fmt.Errorf("updated XDR is not a v1 transaction")
	}

	updatedTx, err = updatedTx.Sign(networkPassphrase, signer)
	if err != nil {
		return fmt.Errorf("sign soroban transaction: %w", err)
	}

	b64Final, err := updatedTx.Base64()
	if err != nil {
		return fmt.Errorf("marshal soroban transaction: %w", err)
	}
	if SubmitAllAndPoll(ctx, logger, rpc, []string{b64Final}) > 0 {
		return fmt.Errorf("soroban transaction failed")
	}
	return nil
}

// isInsufficientFee returns true if errorResultXDR from a SendTransaction
// rejection indicates TxInsufficientFee (surge pricing).
func isInsufficientFee(errorResultXDR string) bool {
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(errorResultXDR, &result); err != nil {
		return false
	}
	return result.Result.Code == xdr.TransactionResultCodeTxInsufficientFee
}

// decodeResultCode unmarshals a base64 TransactionResult XDR and returns the
// TransactionResultCode as a human-readable string.  For fee-bump transactions
// that fail with TxFeeBumpInnerFailed the inner result code is appended so the
// exact cause is visible without inspecting raw XDR.
// Returns "unknown" on any decode error so it is always safe to log.
func decodeResultCode(resultXDR string) string {
	if resultXDR == "" {
		return "unknown"
	}
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &result); err != nil {
		return "decode-error"
	}
	outer := result.Result.Code.String()
	if result.Result.Code == xdr.TransactionResultCodeTxFeeBumpInnerFailed {
		if inner, ok := result.Result.GetInnerResultPair(); ok {
			innerCode := inner.Result.Result.Code.String()
			return outer + " (inner: " + innerCode + ")"
		}
	}
	return outer
}

// logTxFailure logs structured details for a failed on-chain transaction:
// the result code decoded from resultXDR, per-operation result codes (for
// TxFailed, which indicates one or more classic ops failed  -- e.g.
// AccountMergeHasSubEntries when trustlines are still attached), and any
// diagnostic events (particularly useful for Soroban contract failures).
func logTxFailure(logger *log.Entry, hash, resultXDR string, diagnosticEventsXDR []string) {
	l := logger.WithField("hash", hash).WithField("resultCode", decodeResultCode(resultXDR))
	l.Error("transaction failed on-chain")
	for i, s := range decodeOpResults(resultXDR) {
		l.WithField("opIndex", i).Errorf("op result: %s", s)
	}
	for i, ev := range diagnosticEventsXDR {
		l.WithField("event", i).Errorf("diagnostic event: %s", ev)
	}
}

// decodeOpResults extracts per-operation result summaries from a base64
// TransactionResult XDR string, descending into fee-bump inner results when
// the outer code is TxFeeBumpInnerFailed. Returns nil if there are no
// operation-level results to report (e.g. pre-apply rejections).
func decodeOpResults(resultXDR string) []string {
	if resultXDR == "" {
		return nil
	}
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &result); err != nil {
		return nil
	}

	var opResults []xdr.OperationResult
	switch result.Result.Code {
	case xdr.TransactionResultCodeTxSuccess, xdr.TransactionResultCodeTxFailed:
		if rs, ok := result.Result.GetResults(); ok {
			opResults = rs
		}
	case xdr.TransactionResultCodeTxFeeBumpInnerSuccess, xdr.TransactionResultCodeTxFeeBumpInnerFailed:
		if pair, ok := result.Result.GetInnerResultPair(); ok {
			ic := pair.Result.Result.Code
			if ic == xdr.TransactionResultCodeTxSuccess || ic == xdr.TransactionResultCodeTxFailed {
				if rs, ok := pair.Result.Result.GetResults(); ok {
					opResults = rs
				}
			}
		}
	}

	out := make([]string, 0, len(opResults))
	for _, r := range opResults {
		out = append(out, describeOpResult(r))
	}
	return out
}

// describeOpResult converts a single OperationResult to a human-readable
// string. For operations that reached execution (OpInner) it further decodes
// the inner type-specific result code; for pre-execution failures it returns
// the OperationResultCode directly.
func describeOpResult(r xdr.OperationResult) string {
	if r.Code != xdr.OperationResultCodeOpInner {
		return r.Code.String()
	}
	tr, ok := r.GetTr()
	if !ok {
		return "OpInner(no-tr)"
	}
	switch tr.Type {
	case xdr.OperationTypeAccountMerge:
		if m, ok := tr.GetAccountMergeResult(); ok {
			return "AccountMerge:" + m.Code.String()
		}
	case xdr.OperationTypePayment:
		if m, ok := tr.GetPaymentResult(); ok {
			return "Payment:" + m.Code.String()
		}
	case xdr.OperationTypeChangeTrust:
		if m, ok := tr.GetChangeTrustResult(); ok {
			return "ChangeTrust:" + m.Code.String()
		}
	case xdr.OperationTypeCreateAccount:
		if m, ok := tr.GetCreateAccountResult(); ok {
			return "CreateAccount:" + m.Code.String()
		}
	}
	return "OpInner(" + tr.Type.String() + ")"
}

// SubmitAllAndPoll submits pre-encoded XDR envelope strings serially and polls
// all accepted hashes concurrently. Serial submission is required because all
// setup transactions share a single source account (the fee payer) with
// pre-assigned consecutive sequence numbers  -- the node will reject a
// transaction with seq N+1 until seq N is in its mempool. Polling is
// concurrent because each poll blocks for a full ledger close (~5 s) and
// there is no ordering constraint on confirmations.
// It works for both classic and fee-bump envelopes. Each call logs individual
// submission and on-chain failures; the returned int is the count of failures.
func SubmitAllAndPoll(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	envelopes []string,
) int {
	// Phase 1: submit serially so the node receives seq N before seq N+1.
	total := len(envelopes)
	var submitFailed int
	hashes := make([]string, 0, total)
	for i, env := range envelopes {
		resp, err := rpc.SendTransaction(ctx, protocol.SendTransactionRequest{Transaction: env})
		if err != nil {
			logger.WithError(err).Warnf("send transaction %d/%d", i+1, total)
			submitFailed++
			continue
		}
		if resp.ErrorResultXDR != "" {
			logSendTransactionRejection(logger, resp)
			code := decodeResultCode(resp.ErrorResultXDR)
			logger.WithError(fmt.Errorf("rejected: resultCode=%s", code)).Warnf("send transaction %d/%d", i+1, total)
			submitFailed++
			continue
		}
		logger.Debugf("submitted %d/%d hash=%s", i+1, total, resp.Hash)
		hashes = append(hashes, resp.Hash)
	}
	if len(hashes) > 0 {
		logger.Infof("%d/%d transactions submitted, polling for confirmation", len(hashes), total)
	}

	// Phase 2: poll all accepted hashes concurrently, bounded by txPollTimeout.
	// Using a child context ensures the goroutines exit cleanly if the parent
	// context is cancelled OR the poll deadline is exceeded.
	pollCtx, pollCancel := context.WithTimeout(ctx, txPollTimeout)
	defer pollCancel()
	var (
		pollFailed atomic.Int32
		confirmed  atomic.Int32
		pollTotal  = int32(len(hashes))
		pollWG     sync.WaitGroup
	)
	for _, hash := range hashes {
		pollWG.Add(1)
		go func(hash string) {
			defer pollWG.Done()
			result, err := rpc.PollTransaction(pollCtx, hash)
			if err != nil {
				logger.WithError(err).WithField("hash", hash).Warn("poll transaction")
				pollFailed.Add(1)
				return
			}
			if result.Status != protocol.TransactionStatusSuccess {
				logTxFailure(logger, hash, result.ResultXDR, result.DiagnosticEventsXDR)
				pollFailed.Add(1)
				return
			}
			n := confirmed.Add(1)
			logger.Infof("confirmed %d/%d hash=%s", n, pollTotal, hash)
		}(hash)
	}
	pollWG.Wait()

	return submitFailed + int(pollFailed.Load())
}

// logSendTransactionRejection logs rich details returned directly by
// sendTransaction for a rejected submission. This is especially useful for
// Soroban pre-apply failures like TxSorobanInvalid.
func logSendTransactionRejection(logger *log.Entry, resp protocol.SendTransactionResponse) {
	l := logger.WithField("resultCode", decodeResultCode(resp.ErrorResultXDR))
	if resp.Hash != "" {
		l = l.WithField("hash", resp.Hash)
	}
	l.Error("transaction rejected during submission")
	for i, s := range decodeOpResults(resp.ErrorResultXDR) {
		l.WithField("opIndex", i).Errorf("submission op result: %s", s)
	}
	for i, ev := range resp.DiagnosticEventsXDR {
		l.WithField("event", i).Errorf("submission diagnostic event: %s", ev)
	}
	for i, ev := range resp.DiagnosticEventsJSON {
		l.WithField("event", i).Errorf("submission diagnostic event json: %s", string(ev))
	}
	if len(resp.ErrorResultJSON) > 0 {
		l.Errorf("submission error result json: %s", string(resp.ErrorResultJSON))
	}
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
		"resourceFee":      simResp.MinResourceFee,
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
