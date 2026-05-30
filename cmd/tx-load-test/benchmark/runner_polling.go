package benchmark

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	txstate "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// pollWorkerCount returns the number of goroutines for polling getTransaction
// to confirm on-chain inclusion. It scales with the target RPS so the drain
// phase after the attack completes in reasonable time.
func pollWorkerCount(targetRPS int) int {
	return max(80, min((targetRPS*8)/5, 1600))
}

// pollTimeout is the per-transaction deadline for polling getTransaction to a
// terminal state. It intentionally outlives the benchmark transaction expiry
// so the poller can observe txTooLate-style terminal failures instead of
// timing out first.
const pollTimeout = 25 * time.Second

const (
	pollBatchSize      = 25
	pollAttemptTimeout = 5 * time.Second
	pollInitialBackoff = 500 * time.Millisecond
	pollMaxBackoff     = 3500 * time.Millisecond
)

type pollTransactionResult struct {
	response                 protocol.GetTransactionResponse
	lastObservedLatestLedger uint32
}

type pollSchedulerOptions struct {
	maxConcurrency int
	batchSize      int
	timeout        time.Duration
	attemptTimeout time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration
	jitter         bool
}

func defaultPollSchedulerOptions(maxConcurrency int) pollSchedulerOptions {
	return pollSchedulerOptions{
		maxConcurrency: maxConcurrency,
		batchSize:      pollBatchSize,
		timeout:        pollTimeout,
		attemptTimeout: pollAttemptTimeout,
		initialBackoff: pollInitialBackoff,
		maxBackoff:     pollMaxBackoff,
		jitter:         true,
	}.normalized()
}

func (o pollSchedulerOptions) normalized() pollSchedulerOptions {
	if o.maxConcurrency <= 0 {
		o.maxConcurrency = 1
	}
	if o.batchSize <= 0 {
		o.batchSize = 1
	}
	if o.timeout <= 0 {
		o.timeout = pollTimeout
	}
	if o.attemptTimeout <= 0 {
		o.attemptTimeout = pollAttemptTimeout
	}
	if o.initialBackoff <= 0 {
		o.initialBackoff = pollInitialBackoff
	}
	if o.maxBackoff < o.initialBackoff {
		o.maxBackoff = o.initialBackoff
	}
	return o
}

type scheduledPollItem struct {
	item                     pollItem
	submitIndex              uint64
	attempt                  int
	nextPollAt               time.Time
	pollDeadline             time.Time
	lastObservedLatestLedger uint32
}

type pollScheduleHeap []*scheduledPollItem

func (h pollScheduleHeap) Len() int { return len(h) }

func (h pollScheduleHeap) Less(i, j int) bool {
	if h[i].nextPollAt.Equal(h[j].nextPollAt) {
		return h[i].submitIndex < h[j].submitIndex
	}
	return h[i].nextPollAt.Before(h[j].nextPollAt)
}

func (h pollScheduleHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *pollScheduleHeap) Push(x any) {
	*h = append(*h, x.(*scheduledPollItem))
}

func (h *pollScheduleHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

type pollBatchResult struct {
	items   []*scheduledPollItem
	results []transactionPollAttemptResult
	err     error
}

func startPollWorkersWithTrace(ctx context.Context, logger *log.Entry, cfg config.Config, rpc *rpcclient.Client, state *attackState, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder) (int, *sync.WaitGroup) {
	numPollWorkers := pollWorkerCount(cfg.TargetRPS)
	logger.Infof("starting %d poll workers", numPollWorkers)
	return startPollSchedulerWithClient(ctx, logger, newSDKTransactionPollClient(rpc), state, accounts, workload, recorder, defaultPollSchedulerOptions(numPollWorkers))
}

func startPollSchedulerWithClient(ctx context.Context, logger *log.Entry, client transactionPollClient, state *attackState, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder, options pollSchedulerOptions) (int, *sync.WaitGroup) {
	options = options.normalized()
	var pollWg sync.WaitGroup
	pollWg.Go(func() {
		runPollScheduler(ctx, logger, client, state, accounts, workload, recorder, options)
	})
	return options.maxConcurrency, &pollWg
}

func runPollScheduler(ctx context.Context, logger *log.Entry, client transactionPollClient, state *attackState, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder, options pollSchedulerOptions) {
	var queue pollScheduleHeap
	heap.Init(&queue)
	results := make(chan pollBatchResult, options.maxConcurrency)
	hashes := state.hashes
	inputOpen := true
	var submitIndex uint64
	inFlight := 0
	ctxDone := ctx.Done()
	ctxCanceled := false

	for {
		if !inputOpen && queue.Len() == 0 && inFlight == 0 {
			return
		}

		if !ctxCanceled {
			progressed := dispatchDuePollBatches(ctx, logger, client, &queue, results, &inFlight, state, accounts, workload, recorder, options)
			if progressed {
				continue
			}
		}

		var dueTimer *time.Timer
		var due <-chan time.Time
		if !ctxCanceled && inFlight < options.maxConcurrency && queue.Len() > 0 {
			delay := time.Until(queue[0].nextPollAt)
			if delay < 0 {
				delay = 0
			}
			dueTimer = time.NewTimer(delay)
			due = dueTimer.C
		}

		select {
		case item, ok := <-hashes:
			stopPollTimer(dueTimer)
			if !ok {
				inputOpen = false
				hashes = nil
				continue
			}
			scheduled := newScheduledPollItem(item, submitIndex, time.Now(), options.timeout)
			submitIndex++
			if ctxCanceled {
				timeoutScheduledPollItem(logger, state, scheduled, accounts, ctx.Err())
				continue
			}
			heap.Push(&queue, scheduled)
		case result := <-results:
			stopPollTimer(dueTimer)
			inFlight--
			handlePollBatchResult(time.Now(), logger, state, &queue, result, accounts, workload, recorder, options, ctxCanceled)
		case <-due:
		case <-ctxDone:
			stopPollTimer(dueTimer)
			ctxCanceled = true
			ctxDone = nil
			timeoutPendingPollItems(logger, state, &queue, accounts, ctx.Err())
		}
	}
}

func newScheduledPollItem(item pollItem, submitIndex uint64, now time.Time, timeout time.Duration) *scheduledPollItem {
	if item.submittedAt.IsZero() {
		item.submittedAt = now
	}
	return &scheduledPollItem{
		item:         item,
		submitIndex:  submitIndex,
		nextPollAt:   now,
		pollDeadline: item.submittedAt.Add(timeout),
	}
}

func dispatchDuePollBatches(ctx context.Context, logger *log.Entry, client transactionPollClient, queue *pollScheduleHeap, results chan<- pollBatchResult, inFlight *int, state *attackState, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder, options pollSchedulerOptions) bool {
	if *inFlight >= options.maxConcurrency || queue.Len() == 0 {
		return false
	}

	now := time.Now()
	if queue.Len() > 0 && queue.peek().nextPollAt.After(now) {
		return false
	}

	batch := make([]*scheduledPollItem, 0, options.batchSize)
	progressed := false
	for len(batch) < options.batchSize && queue.Len() > 0 {
		scheduled := queue.peek()
		if scheduled.nextPollAt.After(now) {
			break
		}
		scheduled = heap.Pop(queue).(*scheduledPollItem)
		progressed = true
		if scheduled.attempt > 0 && !now.Before(scheduled.pollDeadline) {
			timeoutScheduledPollItem(logger, state, scheduled, accounts, fmt.Errorf("poll deadline exceeded"))
			continue
		}
		scheduled.attempt++
		recordPollRequestTrace(workload, scheduled.item.hash, scheduled.item.rpcID, scheduled.attempt, recorder)
		batch = append(batch, scheduled)
	}

	if len(batch) == 0 {
		return progressed
	}

	*inFlight++
	go runPollBatch(ctx, client, batch, results, options)
	return true
}

func (h pollScheduleHeap) peek() *scheduledPollItem {
	return h[0]
}

func runPollBatch(ctx context.Context, client transactionPollClient, batch []*scheduledPollItem, results chan<- pollBatchResult, options pollSchedulerOptions) {
	requests := make([]transactionPollRequest, len(batch))
	for i, item := range batch {
		requests[i] = transactionPollRequest{ID: pollRequestID(item), Hash: item.item.hash}
	}

	if client == nil {
		results <- pollBatchResult{items: batch, err: fmt.Errorf("missing transaction poll client")}
		return
	}

	attemptCtx := ctx
	var cancel context.CancelFunc
	if options.attemptTimeout > 0 {
		attemptCtx, cancel = context.WithTimeout(ctx, options.attemptTimeout)
	}
	responses, err := client.GetTransactions(attemptCtx, requests)
	if cancel != nil {
		cancel()
	}
	results <- pollBatchResult{items: batch, results: responses, err: err}
}

func pollRequestID(item *scheduledPollItem) int64 {
	return int64(item.submitIndex + 1)
}

func handlePollBatchResult(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, batch pollBatchResult, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder, options pollSchedulerOptions, ctxCanceled bool) {
	if batch.err != nil {
		for _, item := range batch.items {
			recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, nil, batch.err, recorder)
			handlePollAttemptError(now, logger, state, queue, item, accounts, batch.err, options, ctxCanceled)
		}
		return
	}

	resultsByID := make(map[int64]transactionPollAttemptResult, len(batch.results))
	for _, result := range batch.results {
		resultsByID[result.ID] = result
	}

	for _, item := range batch.items {
		result, ok := resultsByID[pollRequestID(item)]
		if !ok {
			err := fmt.Errorf("missing poll response for hash %s", item.item.hash)
			recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, nil, err, recorder)
			handlePollAttemptError(now, logger, state, queue, item, accounts, err, options, ctxCanceled)
			continue
		}
		if result.Hash != "" && result.Hash != item.item.hash {
			err := fmt.Errorf("poll response hash mismatch: request=%s response=%s", item.item.hash, result.Hash)
			recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, nil, err, recorder)
			handlePollAttemptError(now, logger, state, queue, item, accounts, err, options, ctxCanceled)
			continue
		}
		if result.Err != nil {
			recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, nil, result.Err, recorder)
			handlePollAttemptError(now, logger, state, queue, item, accounts, result.Err, options, ctxCanceled)
			continue
		}

		resp := result.Response
		recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, &resp, nil, recorder)
		handlePollAttemptResponse(now, logger, state, queue, item, &resp, accounts, options, ctxCanceled)
	}
}

func handlePollAttemptResponse(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, item *scheduledPollItem, resp *protocol.GetTransactionResponse, accounts accountLeaseManager, options pollSchedulerOptions, ctxCanceled bool) {
	if resp.LatestLedger > 0 {
		item.lastObservedLatestLedger = resp.LatestLedger
	}

	switch resp.Status {
	case protocol.TransactionStatusSuccess, protocol.TransactionStatusFailed:
		handlePollResponse(logger, state, item.item, resp, accounts)
	case "":
		requeueOrTimeoutPollItem(now, logger, state, queue, item, accounts, fmt.Errorf("empty transaction status"), options, ctxCanceled)
	default:
		requeueOrTimeoutPollItem(now, logger, state, queue, item, accounts, nil, options, ctxCanceled)
	}
}

func handlePollAttemptError(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, item *scheduledPollItem, accounts accountLeaseManager, err error, options pollSchedulerOptions, ctxCanceled bool) {
	requeueOrTimeoutPollItem(now, logger, state, queue, item, accounts, err, options, ctxCanceled)
}

func requeueOrTimeoutPollItem(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, item *scheduledPollItem, accounts accountLeaseManager, err error, options pollSchedulerOptions, ctxCanceled bool) {
	if ctxCanceled || !now.Before(item.pollDeadline) {
		if err == nil {
			err = fmt.Errorf("poll deadline exceeded")
		}
		timeoutScheduledPollItem(logger, state, item, accounts, err)
		return
	}
	item.nextPollAt = now.Add(pollBackoff(item, options))
	heap.Push(queue, item)
}

func pollBackoff(item *scheduledPollItem, options pollSchedulerOptions) time.Duration {
	backoff := options.initialBackoff
	for range max(item.attempt-1, 0) {
		backoff *= 2
		if backoff >= options.maxBackoff {
			backoff = options.maxBackoff
			break
		}
	}
	if !options.jitter || backoff <= 0 {
		return backoff
	}
	jitterWindow := int64(min(backoff/10, 250*time.Millisecond))
	if jitterWindow <= 0 {
		return backoff
	}
	seed := item.submitIndex*1103515245 + uint64(item.attempt)*12345
	jitter := time.Duration(int64(seed%uint64(jitterWindow*2+1)) - jitterWindow)
	backoff += jitter
	if backoff > options.maxBackoff {
		return options.maxBackoff
	}
	if backoff < 0 {
		return 0
	}
	return backoff
}

func timeoutPendingPollItems(logger *log.Entry, state *attackState, queue *pollScheduleHeap, accounts accountLeaseManager, err error) {
	for queue.Len() > 0 {
		timeoutScheduledPollItem(logger, state, heap.Pop(queue).(*scheduledPollItem), accounts, err)
	}
}

func timeoutScheduledPollItem(logger *log.Entry, state *attackState, item *scheduledPollItem, accounts accountLeaseManager, err error) {
	state.ledgerStats.observeTimeout(item.item.submitLatestLedger, item.lastObservedLatestLedger)
	atomic.AddUint64(&state.pollErr, 1)
	if logger != nil {
		logger.WithError(err).Debugf("poll failed: hash=%s", item.item.hash)
	}
	atomic.AddUint64(&state.ambiguous, 1)
	releaseAmbiguous(accounts, item.item.rpcID)
}

func stopPollTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func handlePollResponse(logger *log.Entry, state *attackState, item pollItem, resp *protocol.GetTransactionResponse, accounts accountLeaseManager) {
	switch resp.Status {
	case protocol.TransactionStatusSuccess:
		atomic.AddUint64(&state.included, 1)
		state.e2eStats.observe(time.Since(item.submittedAt))
		state.ledgerStats.observeFinality(item.submitLatestLedger, resp.Ledger)
		releaseConsumed(accounts, item.rpcID)
	case protocol.TransactionStatusFailed:
		atomic.AddUint64(&state.onChainFail, 1)
		state.e2eStats.observe(time.Since(item.submittedAt))
		state.ledgerStats.observeFinality(item.submitLatestLedger, resp.Ledger)
		releaseConsumed(accounts, item.rpcID)
		code := ledger.DecodeTransactionResultCode(resp.ResultXDR)
		state.onChainErrorCodes.inc(code)
		for _, opResult := range txstate.DecodeOperationResults(resp.ResultXDR) {
			state.onChainOpResults.inc(opResult)
		}
		if summary := summarizeDiagnosticEvents(resp.DiagnosticEventsXDR); summary != "" {
			state.onChainDiagnostics.inc(summary)
		}
		entry := logger.WithField("hash", item.hash).WithField("resultCode", code)
		entry.Debug("on-chain failure")
		for i, ev := range resp.DiagnosticEventsXDR {
			entry.Debugf("diagnostic event[%d]: %s", i, ev)
		}
	}
}

func waitForPollWorkers(logger *log.Entry, queued uint64, numPollWorkers int, pollWg *sync.WaitGroup, state *attackState) time.Duration {
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
			return drainTimeout
		case <-drainDeadline:
			logger.Warnf("poll workers still running after %s -- abandoning", drainTimeout)
			return drainTimeout
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
