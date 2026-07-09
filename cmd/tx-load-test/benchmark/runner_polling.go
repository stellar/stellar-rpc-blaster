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
	pollBatchSize = 25
	// pollAttemptTimeout bounds a single batch's getTransaction round trip.
	// Kept just above the observed median getTransaction latency under load so
	// slow-but-valid calls complete instead of being cancelled mid-flight and
	// retried (which wastes the call and learns nothing). Still well under
	// pollTimeout so an item can make several attempts before its deadline.
	pollAttemptTimeout = 6 * time.Second
	pollInitialBackoff = 500 * time.Millisecond
	pollMaxBackoff     = 3500 * time.Millisecond
)

const (
	// estimatedLedgerCloseInterval is the assumed wall-clock interval between
	// ledger closes. Used to predict when the next close after a submit will
	// happen so the first poll attempt lands inside the index window rather
	// than racing it. Mirrors state.BenchmarkLedgerCloseSeconds; not imported
	// to keep this package self-contained, but the two must agree.
	estimatedLedgerCloseInterval = 5 * time.Second
	// estimatedRPCIndexingLag is the typical delay between a ledger closing on
	// core and stellar-rpc surfacing a getTransaction result for txs in that
	// ledger. Tuned for a stellar-rpc running on the same host as the bench
	// (low millisecond network overhead); raise for shared/remote RPC nodes
	// where indexing typically settles around 1.5 s.
	estimatedRPCIndexingLag = 1 * time.Second
	// minFirstPollDelay is the floor for the first poll attempt when we lack
	// a useful close-time prediction (legacy poll items, oddly-shaped PENDING
	// responses, or a submit that landed just before a close). Without this
	// floor we'd issue the first poll within milliseconds of submit and waste
	// a guaranteed-NOT_FOUND RPC round trip while the ledger hasn't closed.
	minFirstPollDelay = 2 * time.Second
)

const (
	// ledgerGateRecheckFloor bounds how soon a gated (deferred) item can be
	// reconsidered. A re-poll can only yield a new answer once a new ledger has
	// closed, so deferred items are scheduled toward the next expected close;
	// this floor keeps a stale/absent close-time hint from spinning the queue.
	ledgerGateRecheckFloor = 500 * time.Millisecond
	// ledgerClockStaleness is how long the shared ledger clock may go without an
	// update before the fallback ticker spends a getLatestLedger call. In steady
	// state getTransaction responses refresh the clock well within this window,
	// so the (heavyweight) ticker call is skipped; it only fires during the
	// drain tail when poll traffic has dried up.
	ledgerClockStaleness = estimatedLedgerCloseInterval + 2*time.Second
	// ledgerTickerLateRetry is the short wait the ticker uses when a close ran
	// late (the sequence did not advance), so a slightly-late close is caught
	// promptly instead of after another full interval.
	ledgerTickerLateRetry = 1 * time.Second
)

// ledgerClock holds the highest network ledger sequence (and its close time)
// observed from any source -- every getTransaction response feeds it for free,
// and the fallback ticker tops it up during the drain tail. The poll scheduler
// reads it to gate re-polls: a transaction's terminal status can only change at
// a ledger close, so an item is only worth re-polling once the clock has
// advanced past the ledger that item last observed. Concurrent writers (the
// scheduler goroutine processing responses, and the ticker goroutine) update it
// via monotonic CAS; reads are lock-free.
type ledgerClock struct {
	sequence   atomic.Uint32
	closeUnix  atomic.Int64
	lastUpdate atomic.Int64 // unix nanos of the most recent advancing update
}

// observe records a (sequence, closeUnix) sample, keeping the max sequence. now
// is passed in so callers can use a single clock reading; it stamps lastUpdate
// only when the sequence actually advances.
func (c *ledgerClock) observe(sequence uint32, closeUnix int64, now time.Time) {
	for {
		cur := c.sequence.Load()
		if sequence <= cur {
			return
		}
		if c.sequence.CompareAndSwap(cur, sequence) {
			if closeUnix > 0 {
				c.closeUnix.Store(closeUnix)
			}
			c.lastUpdate.Store(now.UnixNano())
			return
		}
	}
}

func (c *ledgerClock) latestSequence() uint32 { return c.sequence.Load() }
func (c *ledgerClock) latestCloseUnix() int64 { return c.closeUnix.Load() }

// fresh reports whether the clock was advanced within ledgerClockStaleness of
// now -- i.e. whether poll responses are keeping it current without help.
func (c *ledgerClock) fresh(now time.Time) bool {
	last := c.lastUpdate.Load()
	if last == 0 {
		return false
	}
	return now.Sub(time.Unix(0, last)) < ledgerClockStaleness
}

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
	// firstPollDelay is the floor for the first poll attempt when no
	// LatestLedgerCloseTime hint is available. Production uses
	// minFirstPollDelay; tests can leave it zero for immediate-poll behavior.
	firstPollDelay time.Duration
	// ledgerGated enables the ledger-advance re-poll gate: an item is only
	// re-polled once the shared ledger clock has advanced past the ledger that
	// item last observed. Production enables it; tests leave it off (zero) to
	// keep the unconditional time-based behavior unless explicitly exercised.
	ledgerGated bool
	jitter      bool
}

func defaultPollSchedulerOptions(maxConcurrency int) pollSchedulerOptions {
	return pollSchedulerOptions{
		maxConcurrency: maxConcurrency,
		batchSize:      pollBatchSize,
		timeout:        pollTimeout,
		attemptTimeout: pollAttemptTimeout,
		initialBackoff: pollInitialBackoff,
		maxBackoff:     pollMaxBackoff,
		firstPollDelay: minFirstPollDelay,
		ledgerGated:    true,
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
	if o.firstPollDelay < 0 {
		o.firstPollDelay = 0
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
	// lastObservedCloseUnix is the close time (unix seconds) of the latest
	// ledger this item's most recent response reported. Used to schedule the
	// next re-poll toward the following expected close so the item sleeps
	// through the inter-close interval instead of waking to be gate-deferred.
	lastObservedCloseUnix int64
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

	clock := &ledgerClock{}
	// Start the fallback latest-ledger ticker only when gating is on and the
	// client can report the latest ledger. It stays dormant while poll
	// responses keep the clock fresh and only spends a getLatestLedger call
	// during the drain tail. Stop it when the scheduler returns.
	if options.ledgerGated {
		if observer, ok := client.(latestLedgerObserver); ok {
			tickerStop := make(chan struct{})
			defer close(tickerStop)
			go runLatestLedgerTicker(ctx, tickerStop, observer, clock)
		}
	}

	for {
		if !inputOpen && queue.Len() == 0 && inFlight == 0 {
			return
		}

		if !ctxCanceled {
			progressed := dispatchDuePollBatches(ctx, logger, client, &queue, results, &inFlight, state, accounts, workload, recorder, options, clock)
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
			scheduled := newScheduledPollItem(item, submitIndex, time.Now(), options)
			submitIndex++
			if ctxCanceled {
				timeoutScheduledPollItem(logger, state, scheduled, accounts, ctx.Err())
				continue
			}
			heap.Push(&queue, scheduled)
		case result := <-results:
			stopPollTimer(dueTimer)
			inFlight--
			handlePollBatchResult(time.Now(), logger, state, &queue, result, accounts, workload, recorder, options, clock, ctxCanceled)
		case <-due:
		case <-ctxDone:
			stopPollTimer(dueTimer)
			ctxCanceled = true
			ctxDone = nil
			timeoutPendingPollItems(logger, state, &queue, accounts, ctx.Err())
		}
	}
}

func newScheduledPollItem(item pollItem, submitIndex uint64, now time.Time, options pollSchedulerOptions) *scheduledPollItem {
	if item.submittedAt.IsZero() {
		item.submittedAt = now
	}
	return &scheduledPollItem{
		item:         item,
		submitIndex:  submitIndex,
		nextPollAt:   firstPollAt(item, now, options.firstPollDelay),
		pollDeadline: item.submittedAt.Add(options.timeout),
	}
}

// firstPollAt returns when the first poll attempt for item should fire. It
// targets the moment the next ledger close + RPC indexing window opens --
// the earliest wall-clock instant getTransaction can return a terminal
// status. The submitLatestLedgerCloseTime hint from sendTransaction tells us
// when the previous close happened; the next close is one
// estimatedLedgerCloseInterval later, plus an estimatedRPCIndexingLag
// cushion. When that prediction is in the past (submit landed just before
// the next close fired, or no hint was provided) we fall back to a floor of
// now + minDelay so we still skip the guaranteed-NOT_FOUND polls.
func firstPollAt(item pollItem, now time.Time, minDelay time.Duration) time.Time {
	floor := now.Add(minDelay)
	if item.submitLatestLedgerCloseTime <= 0 {
		return floor
	}
	predicted := time.Unix(item.submitLatestLedgerCloseTime, 0).
		Add(estimatedLedgerCloseInterval).
		Add(estimatedRPCIndexingLag)
	if predicted.Before(floor) {
		return floor
	}
	return predicted
}

// ledgerGateBlocks reports whether a re-poll of item should be deferred because
// no new ledger has closed since it was last observed. It blocks only when both
// the item and the clock have a known ledger and the clock has not advanced past
// the item's last-observed ledger. When the clock is unknown (no observation
// yet) or the item has no observed ledger (e.g. its last attempt errored), it
// does not block -- falling back to time-based scheduling.
func ledgerGateBlocks(item *scheduledPollItem, clock *ledgerClock) bool {
	if clock == nil {
		return false
	}
	latest := clock.latestSequence()
	if latest == 0 || item.lastObservedLatestLedger == 0 {
		return false
	}
	return latest <= item.lastObservedLatestLedger
}

// nextCloseProjection returns when a deferred item should next be considered:
// the close after the one it last observed, plus the indexing cushion, floored
// so a missing or stale close-time hint cannot spin the queue.
func nextCloseProjection(now time.Time, lastObservedCloseUnix int64) time.Time {
	floor := now.Add(ledgerGateRecheckFloor)
	if lastObservedCloseUnix <= 0 {
		return floor
	}
	projected := time.Unix(lastObservedCloseUnix, 0).
		Add(estimatedLedgerCloseInterval).
		Add(estimatedRPCIndexingLag)
	if projected.Before(floor) {
		return floor
	}
	return projected
}

func dispatchDuePollBatches(ctx context.Context, logger *log.Entry, client transactionPollClient, queue *pollScheduleHeap, results chan<- pollBatchResult, inFlight *int, state *attackState, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder, options pollSchedulerOptions, clock *ledgerClock) bool {
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
		// Ledger-advance gate: a re-poll can only return a new answer once a new
		// ledger has closed. If the shared clock has not advanced past the ledger
		// this item last observed, defer it toward the next expected close
		// instead of spending a (whole-ledger-decoding) getTransaction call. The
		// first attempt is never gated -- it is timed by firstPollAt.
		if options.ledgerGated && scheduled.attempt > 0 && ledgerGateBlocks(scheduled, clock) {
			scheduled.nextPollAt = nextCloseProjection(now, scheduled.lastObservedCloseUnix)
			heap.Push(queue, scheduled)
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

func handlePollBatchResult(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, batch pollBatchResult, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder, options pollSchedulerOptions, clock *ledgerClock, ctxCanceled bool) {
	if batch.err != nil {
		for _, item := range batch.items {
			recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, nil, batch.err, recorder)
			handlePollAttemptError(now, logger, state, queue, item, accounts, options, clock, batch.err, ctxCanceled)
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
			handlePollAttemptError(now, logger, state, queue, item, accounts, options, clock, err, ctxCanceled)
			continue
		}
		if result.Hash != "" && result.Hash != item.item.hash {
			err := fmt.Errorf("poll response hash mismatch: request=%s response=%s", item.item.hash, result.Hash)
			recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, nil, err, recorder)
			handlePollAttemptError(now, logger, state, queue, item, accounts, options, clock, err, ctxCanceled)
			continue
		}
		if result.Err != nil {
			recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, nil, result.Err, recorder)
			handlePollAttemptError(now, logger, state, queue, item, accounts, options, clock, result.Err, ctxCanceled)
			continue
		}

		resp := result.Response
		recordPollResponseTrace(workload, item.item.hash, item.item.rpcID, item.attempt, &resp, nil, recorder)
		handlePollAttemptResponse(now, logger, state, queue, item, &resp, accounts, options, clock, ctxCanceled)
	}
}

func handlePollAttemptResponse(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, item *scheduledPollItem, resp *protocol.GetTransactionResponse, accounts accountLeaseManager, options pollSchedulerOptions, clock *ledgerClock, ctxCanceled bool) {
	if resp.LatestLedger > 0 {
		item.lastObservedLatestLedger = resp.LatestLedger
		if resp.LatestLedgerCloseTime > 0 {
			item.lastObservedCloseUnix = resp.LatestLedgerCloseTime
		}
		// Feed the shared clock for free from every response; this is what keeps
		// the ledger-advance gate current in steady state without the ticker.
		clock.observe(resp.LatestLedger, resp.LatestLedgerCloseTime, now)
	}

	switch resp.Status {
	case protocol.TransactionStatusSuccess, protocol.TransactionStatusFailed:
		handlePollResponse(logger, state, item.item, resp, accounts)
	case "":
		requeueOrTimeoutPollItem(now, logger, state, queue, item, accounts, options, clock, fmt.Errorf("empty transaction status"), ctxCanceled)
	default:
		requeueOrTimeoutPollItem(now, logger, state, queue, item, accounts, options, clock, nil, ctxCanceled)
	}
}

func handlePollAttemptError(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, item *scheduledPollItem, accounts accountLeaseManager, options pollSchedulerOptions, clock *ledgerClock, err error, ctxCanceled bool) {
	requeueOrTimeoutPollItem(now, logger, state, queue, item, accounts, options, clock, err, ctxCanceled)
}

func requeueOrTimeoutPollItem(now time.Time, logger *log.Entry, state *attackState, queue *pollScheduleHeap, item *scheduledPollItem, accounts accountLeaseManager, options pollSchedulerOptions, clock *ledgerClock, err error, ctxCanceled bool) {
	if ctxCanceled || !now.Before(item.pollDeadline) {
		if err == nil {
			err = fmt.Errorf("poll deadline exceeded")
		}
		timeoutScheduledPollItem(logger, state, item, accounts, err)
		return
	}
	next := now.Add(pollBackoff(item, options))
	// When gating is on and a new ledger has not yet closed since this item was
	// last observed, there is no point waking before the next expected close --
	// push the wake-up out to it so the item sleeps through the inter-close
	// interval rather than waking only to be gate-deferred.
	if options.ledgerGated && ledgerGateBlocks(item, clock) {
		if aligned := nextCloseProjection(now, item.lastObservedCloseUnix); aligned.After(next) {
			next = aligned
		}
	}
	item.nextPollAt = next
	heap.Push(queue, item)
}

func pollBackoff(item *scheduledPollItem, options pollSchedulerOptions) time.Duration {
	// Linear arithmetic-step backoff: attempt N waits N * initialBackoff,
	// capped at maxBackoff. With the prediction-based first poll already
	// landing inside the ledger close + index window, exponential growth is
	// unnecessary -- a linear step lets subsequent attempts spread out
	// gradually as the tx ages without slipping a full ledger close behind.
	attempts := max(item.attempt, 1)
	backoff := time.Duration(attempts) * options.initialBackoff
	if backoff > options.maxBackoff {
		backoff = options.maxBackoff
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

// runLatestLedgerTicker is the fallback that keeps the shared ledger clock
// advancing when poll traffic is too sparse to do so (the drain tail). It paces
// to the close cadence -- waking shortly after the next close is expected from
// the last known close time -- and skips the (whole-ledger-marshaling)
// getLatestLedger call entirely when poll responses have refreshed the clock
// within ledgerClockStaleness. A close that ran late (sequence did not advance)
// triggers a short retry instead of waiting another full interval.
func runLatestLedgerTicker(ctx context.Context, stop <-chan struct{}, observer latestLedgerObserver, clock *ledgerClock) {
	for {
		var delay time.Duration
		if closeUnix := clock.latestCloseUnix(); closeUnix > 0 {
			next := time.Unix(closeUnix, 0).Add(estimatedLedgerCloseInterval).Add(estimatedRPCIndexingLag)
			delay = time.Until(next)
		}
		if delay < ledgerGateRecheckFloor {
			delay = ledgerGateRecheckFloor
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}

		// Skip the heavy call while poll responses are keeping the clock fresh.
		if clock.fresh(time.Now()) {
			continue
		}
		before := clock.latestSequence()
		seq, closeUnix, err := observer.GetLatestLedgerSeq(ctx)
		if err != nil {
			continue
		}
		clock.observe(seq, closeUnix, time.Now())
		if seq <= before {
			// Close ran late; recheck soon rather than after a full interval.
			timer := time.NewTimer(ledgerTickerLateRetry)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
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
	// A poll timeout leaves the on-chain outcome genuinely ambiguous: the tx may
	// have been included (sequence consumed) or evicted (sequence untouched), and
	// the poller simply could not confirm in time. Neither optimistic assumption
	// is safe -- rolling the sequence back reuses a consumed number and assuming
	// consumed skips one -- both yield txBAD_SEQ. Route through ReleaseAmbiguous
	// so the recovery loop reloads chain truth before the account is reused. The
	// recovery loop is parallelized (see runRecoveryLoop) so this stays cheap.
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
