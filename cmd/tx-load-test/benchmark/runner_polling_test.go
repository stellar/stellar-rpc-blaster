package benchmark

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/require"
)

type scriptedPollResponse struct {
	response protocol.GetTransactionResponse
	err      error
}

type scriptedPollClient struct {
	mu        sync.Mutex
	scripts   map[string][]scriptedPollResponse
	batches   [][]string
	callCount map[string]int
}

func newScriptedPollClient(scripts map[string][]scriptedPollResponse) *scriptedPollClient {
	return &scriptedPollClient{scripts: scripts, callCount: make(map[string]int)}
}

func (c *scriptedPollClient) GetTransactions(_ context.Context, requests []transactionPollRequest) ([]transactionPollAttemptResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	batch := make([]string, len(requests))
	results := make([]transactionPollAttemptResult, len(requests))
	for i, request := range requests {
		batch[i] = request.Hash
		attempt := c.callCount[request.Hash]
		c.callCount[request.Hash]++
		responses := c.scripts[request.Hash]
		if attempt >= len(responses) {
			results[i] = transactionPollAttemptResult{ID: request.ID, Hash: request.Hash, Err: fmt.Errorf("unexpected poll attempt for %s", request.Hash)}
			continue
		}
		results[i] = transactionPollAttemptResult{
			ID:       request.ID,
			Hash:     request.Hash,
			Response: responses[attempt].response,
			Err:      responses[attempt].err,
		}
	}
	c.batches = append(c.batches, batch)
	return results, nil
}

func (c *scriptedPollClient) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0)
	for _, batch := range c.batches {
		out = append(out, batch...)
	}
	return out
}

func TestPollSchedulerRequeuesNonTerminalWithoutStarvingNewerDueItems(t *testing.T) {
	state := newAttackState(2)
	now := time.Now()
	state.hashes <- pollItem{hash: "old", rpcID: 11, submittedAt: now, submitLatestLedger: 100}
	state.hashes <- pollItem{hash: "new", rpcID: 22, submittedAt: now, submitLatestLedger: 100}
	close(state.hashes)

	client := newScriptedPollClient(map[string][]scriptedPollResponse{
		"old": {
			{response: protocol.GetTransactionResponse{LatestLedger: 101, TransactionDetails: protocol.TransactionDetails{Status: "NOT_FOUND"}}},
			{response: protocol.GetTransactionResponse{LatestLedger: 103, TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess, Ledger: 103}}},
		},
		"new": {
			{response: protocol.GetTransactionResponse{LatestLedger: 102, TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess, Ledger: 102}}},
		},
	})
	leases := &fakeLeaseManager{}

	_, wg := startPollSchedulerWithClient(context.Background(), nilLogger(), client, state, leases, "soroswap", nil, pollSchedulerOptions{
		maxConcurrency: 1,
		batchSize:      1,
		timeout:        time.Second,
		attemptTimeout: time.Second,
		initialBackoff: 20 * time.Millisecond,
		maxBackoff:     20 * time.Millisecond,
	})
	waitForPollWaitGroup(t, wg)

	require.Equal(t, []string{"old", "new", "old"}, client.calls())
	require.Equal(t, []int64{22, 11}, leases.consumedReleases)
	included, onChainFail, pollErr := state.pollSnapshot()
	require.Equal(t, uint64(2), included)
	require.Equal(t, uint64(0), onChainFail)
	require.Equal(t, uint64(0), pollErr)
}

func TestPollSchedulerAllowsOneFinalAttemptForAlreadyExpiredItem(t *testing.T) {
	state := newAttackState(1)
	state.hashes <- pollItem{hash: "expired", rpcID: 33, submittedAt: time.Now().Add(-time.Second), submitLatestLedger: 100}
	close(state.hashes)

	client := newScriptedPollClient(map[string][]scriptedPollResponse{
		"expired": {
			{response: protocol.GetTransactionResponse{LatestLedger: 105, TransactionDetails: protocol.TransactionDetails{Status: "NOT_FOUND"}}},
		},
	})
	leases := &fakeLeaseManager{}

	_, wg := startPollSchedulerWithClient(context.Background(), nilLogger(), client, state, leases, "soroswap", nil, pollSchedulerOptions{
		maxConcurrency: 1,
		batchSize:      1,
		timeout:        time.Millisecond,
		attemptTimeout: time.Second,
		initialBackoff: time.Millisecond,
		maxBackoff:     time.Millisecond,
	})
	waitForPollWaitGroup(t, wg)

	require.Equal(t, []string{"expired"}, client.calls())
	// A poll timeout leaves the sequence state ambiguous, so the account is
	// released ambiguous (reload chain truth via recovery) rather than reused.
	require.Equal(t, []int64{33}, leases.ambiguousReleases)
	_, _, pollErr := state.pollSnapshot()
	require.Equal(t, uint64(1), pollErr)
	snapshot := state.ledgerStats.snapshot()
	require.Equal(t, []uint32{5}, snapshot.timeoutDistances)
}

func TestFirstPollAtPredictsNextCloseAndIndexWindow(t *testing.T) {
	// Submit at wall-clock t=10s; RPC reports last close was at t=8s (2s ago).
	// Next close expected at t=8+5=13s; indexed ~t=14s after the 1s lag.
	// First poll should fire at exactly t=14s -- a delay of 4s from submit.
	now := time.Unix(10, 0)
	item := pollItem{submittedAt: now, submitLatestLedgerCloseTime: 8}
	got := firstPollAt(item, now, minFirstPollDelay)
	want := time.Unix(8, 0).Add(estimatedLedgerCloseInterval).Add(estimatedRPCIndexingLag)
	require.Equal(t, want, got)
	require.Equal(t, 4*time.Second, got.Sub(now))
}

func TestFirstPollAtFloorsWhenPredictionIsInPast(t *testing.T) {
	// Submit at t=20s, last close was at t=10s (10s ago). The predicted
	// next-close moment (t=15s) is already past; fall back to the floor.
	now := time.Unix(20, 0)
	item := pollItem{submittedAt: now, submitLatestLedgerCloseTime: 10}
	got := firstPollAt(item, now, minFirstPollDelay)
	require.Equal(t, now.Add(minFirstPollDelay), got)
}

func TestFirstPollAtFloorsWhenCloseTimeMissing(t *testing.T) {
	now := time.Unix(100, 0)
	got := firstPollAt(pollItem{submittedAt: now}, now, minFirstPollDelay)
	require.Equal(t, now.Add(minFirstPollDelay), got)
}

func TestFirstPollAtImmediateWhenFloorIsZero(t *testing.T) {
	// Tests pass firstPollDelay=0 to keep their immediate-poll semantics.
	// Without a close-time hint, the result is exactly now.
	now := time.Unix(100, 0)
	got := firstPollAt(pollItem{submittedAt: now}, now, 0)
	require.Equal(t, now, got)
}

func TestPollBackoffIsLinearArithmeticStepCappedAtMax(t *testing.T) {
	opts := pollSchedulerOptions{
		initialBackoff: 500 * time.Millisecond,
		maxBackoff:     3500 * time.Millisecond,
		jitter:         false,
	}.normalized()
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 500 * time.Millisecond},
		{2, 1000 * time.Millisecond},
		{3, 1500 * time.Millisecond},
		{4, 2000 * time.Millisecond},
		{5, 2500 * time.Millisecond},
		{6, 3000 * time.Millisecond},
		{7, 3500 * time.Millisecond},
		{8, 3500 * time.Millisecond},  // capped
		{20, 3500 * time.Millisecond}, // capped
	}
	for _, c := range cases {
		got := pollBackoff(&scheduledPollItem{attempt: c.attempt}, opts)
		require.Equal(t, c.want, got, "attempt %d", c.attempt)
	}
}

func TestPollBackoffJitterStaysWithinTenPercentWindow(t *testing.T) {
	// At cap (3500ms), jitter window = 350ms but capped at 250ms (per code).
	opts := pollSchedulerOptions{
		initialBackoff: 500 * time.Millisecond,
		maxBackoff:     3500 * time.Millisecond,
		jitter:         true,
	}.normalized()
	// Run several distinct (submitIndex, attempt) combos to exercise the
	// jitter seed and confirm bounds.
	for _, attempt := range []int{1, 3, 7, 10} {
		base := time.Duration(min(attempt, 7)) * 500 * time.Millisecond
		jitterCap := min(base/10, 250*time.Millisecond)
		for idx := uint64(0); idx < 20; idx++ {
			got := pollBackoff(&scheduledPollItem{submitIndex: idx, attempt: attempt}, opts)
			require.GreaterOrEqual(t, got, base-jitterCap, "attempt=%d idx=%d", attempt, idx)
			require.LessOrEqual(t, got, base+jitterCap, "attempt=%d idx=%d", attempt, idx)
			require.LessOrEqual(t, got, opts.maxBackoff)
		}
	}
}

func TestNewScheduledPollItemAppliesPredictedFirstPoll(t *testing.T) {
	// End-to-end: a poll item carrying a close-time hint should schedule its
	// first poll at the predicted close+index window, not at "now". This is
	// the production-path assertion that the prediction is wired through.
	now := time.Unix(1000, 0)
	item := pollItem{
		hash:                        "abc",
		submittedAt:                 now,
		submitLatestLedgerCloseTime: 998,
	}
	opts := pollSchedulerOptions{
		timeout:        25 * time.Second,
		firstPollDelay: minFirstPollDelay,
	}.normalized()
	scheduled := newScheduledPollItem(item, 0, now, opts)
	wantFirstPoll := time.Unix(998, 0).Add(estimatedLedgerCloseInterval).Add(estimatedRPCIndexingLag)
	require.Equal(t, wantFirstPoll, scheduled.nextPollAt)
	require.Equal(t, now.Add(25*time.Second), scheduled.pollDeadline)
}

func TestLedgerClockObserveKeepsMaxAndTracksFreshness(t *testing.T) {
	clock := &ledgerClock{}
	require.Equal(t, uint32(0), clock.latestSequence())
	require.False(t, clock.fresh(time.Unix(100, 0)))

	clock.observe(50, 1000, time.Unix(100, 0))
	require.Equal(t, uint32(50), clock.latestSequence())
	require.Equal(t, int64(1000), clock.latestCloseUnix())

	// A lower sequence is ignored (monotonic), and does not refresh.
	clock.observe(40, 999, time.Unix(200, 0))
	require.Equal(t, uint32(50), clock.latestSequence())
	require.Equal(t, int64(1000), clock.latestCloseUnix())

	// A higher sequence advances and refreshes.
	clock.observe(51, 1005, time.Unix(300, 0))
	require.Equal(t, uint32(51), clock.latestSequence())
	require.Equal(t, int64(1005), clock.latestCloseUnix())

	// Freshness is measured from the last advancing update (t=300).
	require.True(t, clock.fresh(time.Unix(300, 0).Add(ledgerClockStaleness-time.Second)))
	require.False(t, clock.fresh(time.Unix(300, 0).Add(ledgerClockStaleness+time.Second)))
}

func TestLedgerGateBlocksUntilLedgerAdvances(t *testing.T) {
	clock := &ledgerClock{}

	// No clock info yet, or no item observation yet: never blocks (fall back to
	// time-based scheduling).
	require.False(t, ledgerGateBlocks(&scheduledPollItem{lastObservedLatestLedger: 100}, clock))
	clock.observe(100, 0, time.Unix(1, 0))
	require.False(t, ledgerGateBlocks(&scheduledPollItem{lastObservedLatestLedger: 0}, clock))

	// Clock has not advanced past the item's last-observed ledger: block.
	require.True(t, ledgerGateBlocks(&scheduledPollItem{lastObservedLatestLedger: 100}, clock))
	require.True(t, ledgerGateBlocks(&scheduledPollItem{lastObservedLatestLedger: 101}, clock))

	// Clock advances: items at/under the new ledger become eligible.
	clock.observe(101, 0, time.Unix(2, 0))
	require.False(t, ledgerGateBlocks(&scheduledPollItem{lastObservedLatestLedger: 100}, clock))
	require.True(t, ledgerGateBlocks(&scheduledPollItem{lastObservedLatestLedger: 101}, clock))
}

func TestNextCloseProjectionAlignsToNextCloseWithFloor(t *testing.T) {
	now := time.Unix(1000, 0)
	// No close-time hint: fall back to the recheck floor.
	require.Equal(t, now.Add(ledgerGateRecheckFloor), nextCloseProjection(now, 0))

	// Last close at t=998; next close+index ~ 998+5+1 = 1004, which is after the
	// floor, so use it.
	got := nextCloseProjection(now, 998)
	want := time.Unix(998, 0).Add(estimatedLedgerCloseInterval).Add(estimatedRPCIndexingLag)
	require.Equal(t, want, got)

	// Last close far in the past: projection is before the floor, so floor wins.
	require.Equal(t, now.Add(ledgerGateRecheckFloor), nextCloseProjection(now, 100))
}

func TestPollSchedulerGatedDefersRepollUntilLedgerAdvances(t *testing.T) {
	// One tx that stays NOT_FOUND at the same ledger across early polls, then
	// SUCCEEDS once the ledger advances. With the gate on, the scheduler must
	// not burn repeated polls while the ledger is unchanged: total attempts
	// should be small (one per observed ledger), not one per backoff tick.
	state := newAttackState(1)
	now := time.Now()
	state.hashes <- pollItem{hash: "tx", rpcID: 7, submittedAt: now, submitLatestLedger: 100}
	close(state.hashes)

	client := newScriptedPollClient(map[string][]scriptedPollResponse{
		"tx": {
			// attempt 1 @ ledger 100 -> NOT_FOUND
			{response: protocol.GetTransactionResponse{LatestLedger: 100, TransactionDetails: protocol.TransactionDetails{Status: "NOT_FOUND"}}},
			// attempt 2 @ ledger 101 -> SUCCESS (gate should have waited for the advance)
			{response: protocol.GetTransactionResponse{LatestLedger: 101, TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess, Ledger: 101}}},
			// safety net if the gate misbehaves and over-polls:
			{response: protocol.GetTransactionResponse{LatestLedger: 101, TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess, Ledger: 101}}},
		},
	})
	leases := &fakeLeaseManager{}

	_, wg := startPollSchedulerWithClient(context.Background(), nilLogger(), client, state, leases, "sac-transfer", nil, pollSchedulerOptions{
		maxConcurrency: 1,
		batchSize:      1,
		timeout:        200 * time.Millisecond,
		attemptTimeout: time.Second,
		initialBackoff: 5 * time.Millisecond,
		maxBackoff:     5 * time.Millisecond,
		ledgerGated:    true,
	})
	waitForPollWaitGroup(t, wg)

	// The clock only advances via the responses themselves here (scripted client
	// has no latest-ledger observer). After attempt 1 reports ledger 100, the
	// item is gated at ledger 100; nothing else advances the clock, so the item
	// stays deferred until its deadline and then times out -- i.e. the gate
	// correctly suppresses redundant same-ledger polls.
	calls := client.calls()
	require.Equal(t, []string{"tx"}, calls, "gate must suppress same-ledger re-polls; got %v", calls)
	_, _, pollErr := state.pollSnapshot()
	require.Equal(t, uint64(1), pollErr)
}

func waitForPollWaitGroup(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poll scheduler did not finish")
	}
}
