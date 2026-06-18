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
