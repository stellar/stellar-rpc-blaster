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
