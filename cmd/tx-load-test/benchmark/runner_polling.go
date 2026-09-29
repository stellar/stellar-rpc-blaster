package benchmark

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

// pollWorkerCount returns the number of goroutines for polling getTransaction
// to confirm on-chain inclusion. It scales with the target RPS so the drain
// phase after the attack completes in reasonable time.
func pollWorkerCount(targetRPS int) int {
	return max(20, min(targetRPS/5, 200))
}

// pollTimeout is the per-transaction deadline for polling getTransaction to a
// terminal state. Five ledgers at ~5 s each gives plenty of margin.
const pollTimeout = 30 * time.Second

func startPollWorkers(ctx context.Context, logger *log.Entry, cfg config.Config, rpc *rpcclient.Client, state *attackState, resetSeq SequenceResetFunc) (int, *sync.WaitGroup) {
	numPollWorkers := pollWorkerCount(cfg.TargetRPS)
	logger.Infof("starting %d poll workers", numPollWorkers)
	var pollWg sync.WaitGroup
	for range numPollWorkers {
		pollWg.Go(func() {
			pollTransactions(ctx, logger, rpc, state, resetSeq)
		})
	}
	return numPollWorkers, &pollWg
}

func pollTransactions(ctx context.Context, logger *log.Entry, rpc *rpcclient.Client, state *attackState, resetSeq SequenceResetFunc) {
	for item := range state.hashes {
		pollCtx, pollCancel := context.WithTimeout(ctx, pollTimeout)
		resp, err := rpc.PollTransaction(pollCtx, item.hash)
		pollCancel()
		if err != nil {
			atomic.AddUint64(&state.pollErr, 1)
			logger.WithError(err).Debugf("poll failed: hash=%s", item.hash)
			state.resetSequence(resetSeq, item.rpcID)
			continue
		}
		handlePollResponse(logger, state, item, &resp)
	}
}

func handlePollResponse(logger *log.Entry, state *attackState, item pollItem, resp *protocol.GetTransactionResponse) {
	switch resp.Status {
	case protocol.TransactionStatusSuccess:
		atomic.AddUint64(&state.included, 1)
		state.e2eStats.observe(time.Since(item.submittedAt))
	case protocol.TransactionStatusFailed:
		atomic.AddUint64(&state.onChainFail, 1)
		state.e2eStats.observe(time.Since(item.submittedAt))
		code := ledger.DecodeTransactionResultCode(resp.ResultXDR)
		entry := logger.WithField("hash", item.hash).WithField("resultCode", code)
		entry.Debug("on-chain failure")
		for i, ev := range resp.DiagnosticEventsXDR {
			entry.Debugf("diagnostic event[%d]: %s", i, ev)
		}
	}
}

func waitForPollWorkers(logger *log.Entry, queued uint64, numPollWorkers int, pollWg *sync.WaitGroup, state *attackState) {
	drainTimeout := estimatePollDrainTimeout(queued, numPollWorkers)
	logger.Infof("waiting up to %s for %d poll workers to drain %d queued txs", drainTimeout, numPollWorkers, queued)

	pollDone := make(chan struct{})
	go func() {
		pollWg.Wait()
		close(pollDone)
	}()

	progressTicker := time.NewTicker(10 * time.Second)
	defer progressTicker.Stop()
	drainDeadline := time.After(drainTimeout)

	for {
		select {
		case <-pollDone:
			return
		case <-drainDeadline:
			logger.Warnf("poll workers still running after %s -- abandoning", drainTimeout)
			return
		case <-progressTicker.C:
			included, onChainFail, pollErr := state.pollSnapshot()
			settled := included + onChainFail + pollErr
			remaining := queued - settled
			if remaining > queued {
				remaining = 0
			}
			logger.Infof("poll progress -- settled=%d (included=%d failed=%d pollErr=%d) remaining=%d",
				settled, included, onChainFail, pollErr, remaining)
		}
	}
}

func estimatePollDrainTimeout(queued uint64, numPollWorkers int) time.Duration {
	batchesNeeded := (queued + uint64(numPollWorkers) - 1) / uint64(numPollWorkers)
	const estimatedAvgPoll = 3 * time.Second
	drainTimeout := time.Duration(batchesNeeded)*estimatedAvgPoll + 2*time.Minute
	if drainTimeout < 3*time.Minute {
		drainTimeout = 3 * time.Minute
	}
	return drainTimeout
}
