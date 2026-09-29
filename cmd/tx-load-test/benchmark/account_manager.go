package benchmark

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const accountRecoveryRetryDelay = 2 * time.Second

// accountRecoveryWorkers bounds how many poisoned accounts can be reloading
// chain truth (LoadAccount) concurrently. A poll timeout poisons an account and
// queues it for recovery; with a single worker, one slow 30s LoadAccount stalls
// the whole queue and the available pool collapses (the failure mode that drove
// the lease-starvation spiral). A modest pool drains recovery in parallel while
// staying small enough not to pile onto an already-loaded RPC.
const accountRecoveryWorkers = 32

type accountRequirement uint8

const (
	accountRequirementAnySource accountRequirement = iota
	accountRequirementTrustlinedSource
)

type leasedAccount struct {
	RequestID int64
	Account   *keypair.Full
	Sequence  int64
}

type accountLeaseManager interface {
	Acquire(ctx context.Context, requirement accountRequirement) (leasedAccount, error)
	Accounts(requirement accountRequirement) []*keypair.Full
	ReleaseRetryable(requestID int64)
	ReleaseConsumed(requestID int64)
	ReleaseAmbiguous(requestID int64)
}

type managedAccount struct {
	kp           *keypair.Full
	nextSequence int64
	leased       bool
	poisoned     bool
	recovering   bool
}

type activeLease struct {
	accountIndex     int
	assignedSequence int64
}

type accountManager struct {
	mu sync.Mutex

	ctx                    context.Context
	accounts               []managedAccount
	generalPreferred       []int
	trustlinedEligible     []int
	generalCursor          int
	trustlinedCursor       int
	active                 map[int64]activeLease
	nextRequestID          int64
	availabilitySignalChan chan struct{}
	recoveryQueue          chan int
	recoveryRetryDelay     time.Duration
	reloadSequence         func(context.Context, *keypair.Full) (int64, error)
}

func newAccountManager(ctx context.Context, st *state.State) (*accountManager, error) {
	manager := &accountManager{
		ctx:                    ctx,
		active:                 make(map[int64]activeLease),
		availabilitySignalChan: make(chan struct{}, 1),
		recoveryQueue:          make(chan int, max(1, len(st.AccountKPs))),
		recoveryRetryDelay:     accountRecoveryRetryDelay,
		reloadSequence: func(ctx context.Context, kp *keypair.Full) (int64, error) {
			return loadSequenceNumber(ctx, st, kp)
		},
	}
	if len(st.AccountKPs) == 0 {
		return manager, nil
	}

	sequences, err := loadSequenceNumbers(ctx, st, st.AccountKPs)
	if err != nil {
		return nil, fmt.Errorf("load benchmark account sequence numbers: %w", err)
	}

	trustlinedAccounts := st.SACHolderKPs
	if len(trustlinedAccounts) == 0 {
		trustlinedAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	trustlinedAddresses := make(map[string]struct{}, len(trustlinedAccounts))
	for _, kp := range trustlinedAccounts {
		trustlinedAddresses[kp.Address()] = struct{}{}
	}

	manager.accounts = make([]managedAccount, len(st.AccountKPs))
	for i, kp := range st.AccountKPs {
		manager.accounts[i] = managedAccount{
			kp:           kp,
			nextSequence: sequences[i],
		}
		if _, ok := trustlinedAddresses[kp.Address()]; ok {
			manager.trustlinedEligible = append(manager.trustlinedEligible, i)
			continue
		}
		manager.generalPreferred = append(manager.generalPreferred, i)
	}
	manager.generalPreferred = append(manager.generalPreferred, manager.trustlinedEligible...)

	recoveryWorkers := min(accountRecoveryWorkers, len(manager.accounts))
	if recoveryWorkers < 1 {
		recoveryWorkers = 1
	}
	for range recoveryWorkers {
		go manager.runRecoveryLoop()
	}

	return manager, nil
}

func (m *accountManager) Accounts(requirement accountRequirement) []*keypair.Full {
	m.mu.Lock()
	defer m.mu.Unlock()

	indices := m.indicesForRequirement(requirement)
	accounts := make([]*keypair.Full, 0, len(indices))
	for _, index := range indices {
		accounts = append(accounts, m.accounts[index].kp)
	}
	return accounts
}

func (m *accountManager) Acquire(ctx context.Context, requirement accountRequirement) (leasedAccount, error) {
	for {
		m.mu.Lock()
		accountIndex := m.acquireIndexLocked(requirement)
		if accountIndex >= 0 {
			m.nextRequestID++
			requestID := m.nextRequestID
			account := &m.accounts[accountIndex]
			account.leased = true
			account.nextSequence++
			lease := leasedAccount{
				RequestID: requestID,
				Account:   account.kp,
				Sequence:  account.nextSequence,
			}
			m.active[requestID] = activeLease{
				accountIndex:     accountIndex,
				assignedSequence: lease.Sequence,
			}
			m.mu.Unlock()
			return lease, nil
		}
		signal := m.availabilitySignalChan
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return leasedAccount{}, ctx.Err()
		case <-signal:
		}
	}
}

func (m *accountManager) ReleaseRetryable(requestID int64) {
	m.release(requestID, func(_ int, account *managedAccount, lease activeLease) releaseOutcome {
		account.nextSequence = lease.assignedSequence - 1
		account.leased = false
		return releaseOutcome{signalAvailability: true}
	})
}

func (m *accountManager) ReleaseConsumed(requestID int64) {
	m.release(requestID, func(_ int, account *managedAccount, _ activeLease) releaseOutcome {
		account.leased = false
		return releaseOutcome{signalAvailability: true}
	})
}

func (m *accountManager) ReleaseAmbiguous(requestID int64) {
	m.release(requestID, func(_ int, account *managedAccount, _ activeLease) releaseOutcome {
		account.leased = false
		account.poisoned = true
		if account.recovering {
			return releaseOutcome{}
		}
		account.recovering = true
		return releaseOutcome{enqueueRecovery: true}
	})
}

type releaseOutcome struct {
	signalAvailability bool
	enqueueRecovery    bool
}

func (m *accountManager) release(requestID int64, apply func(accountIndex int, account *managedAccount, lease activeLease) releaseOutcome) {
	if requestID <= 0 {
		return
	}

	m.mu.Lock()
	lease, ok := m.active[requestID]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.active, requestID)
	outcome := apply(lease.accountIndex, &m.accounts[lease.accountIndex], lease)
	m.mu.Unlock()

	if outcome.enqueueRecovery {
		m.enqueueRecovery(lease.accountIndex)
	}

	if outcome.signalAvailability {
		select {
		case m.availabilitySignalChan <- struct{}{}:
		default:
		}
	}
}

func (m *accountManager) enqueueRecovery(accountIndex int) {
	if accountIndex < 0 {
		return
	}
	select {
	case <-m.ctx.Done():
		return
	case m.recoveryQueue <- accountIndex:
	default:
		go func() {
			select {
			case <-m.ctx.Done():
			case m.recoveryQueue <- accountIndex:
			}
		}()
	}
}

func (m *accountManager) runRecoveryLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case accountIndex := <-m.recoveryQueue:
			m.recoverAccount(accountIndex)
		}
	}
}

func (m *accountManager) recoverAccount(accountIndex int) {
	for {
		if err := m.ctx.Err(); err != nil {
			return
		}

		account := m.accountKeypair(accountIndex)
		if account == nil {
			m.finishRecovery(accountIndex, false, 0)
			return
		}

		reloadCtx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		sequence, err := m.reloadSequence(reloadCtx, account)
		cancel()
		if err == nil {
			m.finishRecovery(accountIndex, true, sequence)
			return
		}

		timer := time.NewTimer(m.recoveryRetryDelay)
		select {
		case <-m.ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (m *accountManager) accountKeypair(accountIndex int) *keypair.Full {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accountIndex < 0 || accountIndex >= len(m.accounts) {
		return nil
	}
	if !m.accounts[accountIndex].recovering {
		return nil
	}
	return m.accounts[accountIndex].kp
}

func (m *accountManager) finishRecovery(accountIndex int, recovered bool, sequence int64) {
	m.mu.Lock()
	if accountIndex < 0 || accountIndex >= len(m.accounts) {
		m.mu.Unlock()
		return
	}
	account := &m.accounts[accountIndex]
	if !account.recovering {
		m.mu.Unlock()
		return
	}
	account.recovering = false
	shouldSignal := false
	if recovered {
		account.nextSequence = sequence
		account.poisoned = false
		shouldSignal = true
	}
	m.mu.Unlock()

	if shouldSignal {
		select {
		case m.availabilitySignalChan <- struct{}{}:
		default:
		}
	}
}

func (m *accountManager) acquireIndexLocked(requirement accountRequirement) int {
	indices := m.indicesForRequirement(requirement)
	if len(indices) == 0 {
		return -1
	}

	start := 0
	switch requirement {
	case accountRequirementTrustlinedSource:
		start = m.trustlinedCursor
	default:
		start = m.generalCursor
	}

	for offset := range len(indices) {
		cursor := (start + offset) % len(indices)
		accountIndex := indices[cursor]
		account := &m.accounts[accountIndex]
		if account.leased || account.poisoned {
			continue
		}
		switch requirement {
		case accountRequirementTrustlinedSource:
			m.trustlinedCursor = (cursor + 1) % len(indices)
		default:
			m.generalCursor = (cursor + 1) % len(indices)
		}
		return accountIndex
	}

	return -1
}

func (m *accountManager) indicesForRequirement(requirement accountRequirement) []int {
	switch requirement {
	case accountRequirementTrustlinedSource:
		return m.trustlinedEligible
	default:
		return m.generalPreferred
	}
}

func loadSequenceNumber(ctx context.Context, st *state.State, accountKP *keypair.Full) (int64, error) {
	acct, err := st.RPCClient.LoadAccount(ctx, accountKP.Address())
	if err != nil {
		return 0, err
	}
	return acct.GetSequenceNumber()
}
