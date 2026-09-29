package state

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
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
			code := ledger.DecodeTransactionResultCode(resp.ErrorResultXDR)
			return fmt.Errorf("fee-bump rejected: resultCode=%s", code)
		}

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
