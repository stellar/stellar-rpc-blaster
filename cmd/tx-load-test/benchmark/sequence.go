package benchmark

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type sequenceManager struct {
	counters []atomic.Int64
}

func newSequenceManager(ctx context.Context, st *state.State, accounts []*keypair.Full, label string) (*sequenceManager, error) {
	seqBase, err := loadSequenceNumbers(ctx, st, accounts)
	if err != nil {
		return nil, fmt.Errorf("load %s sequence numbers: %w", label, err)
	}
	manager := &sequenceManager{counters: make([]atomic.Int64, len(accounts))}
	for i, base := range seqBase {
		manager.counters[i].Store(base)
	}
	return manager, nil
}

func (m *sequenceManager) Next(index int) int64 {
	return m.counters[index].Add(1)
}

func (m *sequenceManager) Reset(jsonRPCID int64) {
	if len(m.counters) == 0 || jsonRPCID <= 0 {
		return
	}
	index := int((jsonRPCID - 1) % int64(len(m.counters)))
	m.counters[index].Add(-1)
}

func (m *sequenceManager) ResetFunc() SequenceResetFunc {
	return func(jsonRPCID int64) {
		m.Reset(jsonRPCID)
	}
}

func loadSequenceNumbers(ctx context.Context, st *state.State, accountKPs []*keypair.Full) ([]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	n := len(accountKPs)
	seqNums := make([]int64, n)
	errs := make([]error, n)

	const concurrency = 50
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i := range n {
		i := i
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			acct, err := st.RPCClient.LoadAccount(ctx, accountKPs[i].Address())
			if err != nil {
				errs[i] = err
				return
			}
			seq, err := acct.GetSequenceNumber()
			if err != nil {
				errs[i] = err
				return
			}
			seqNums[i] = seq
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("account[%d] load sequence: %w", i, err)
		}
	}
	return seqNums, nil
}
