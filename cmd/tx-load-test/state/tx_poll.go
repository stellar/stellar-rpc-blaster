package state

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

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
			code := ledger.DecodeTransactionResultCode(resp.ErrorResultXDR)
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

	pollCtx, pollCancel := context.WithTimeout(ctx, txPollTimeout)
	defer pollCancel()
	var (
		pollFailed atomic.Int32
		confirmed  atomic.Int32
		pollTotal  = int32(len(hashes))
		pollWG     sync.WaitGroup
	)
	for _, hash := range hashes {
		hash := hash
		pollWG.Go(func() {
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
		})
	}
	pollWG.Wait()

	return submitFailed + int(pollFailed.Load())
}
