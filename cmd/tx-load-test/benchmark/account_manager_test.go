package benchmark

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"
)

func newTestAccountManager(t *testing.T, generalOnlyCount, trustlinedCount int, baseSequence int64, reloadSequence func(context.Context, *keypair.Full) (int64, error)) (*accountManager, []*keypair.Full) {
	t.Helper()
	total := generalOnlyCount + trustlinedCount
	accounts := make([]*keypair.Full, 0, total)
	managed := make([]managedAccount, 0, total)
	generalPreferred := make([]int, 0, total)
	trustlinedEligible := make([]int, 0, trustlinedCount)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	for i := 0; i < total; i++ {
		kp := keypair.MustRandom()
		accounts = append(accounts, kp)
		managed = append(managed, managedAccount{kp: kp, nextSequence: baseSequence})
	}
	for i := 0; i < generalOnlyCount; i++ {
		generalPreferred = append(generalPreferred, i)
	}
	for i := generalOnlyCount; i < total; i++ {
		trustlinedEligible = append(trustlinedEligible, i)
		generalPreferred = append(generalPreferred, i)
	}

	manager := &accountManager{
		ctx:                    ctx,
		accounts:               managed,
		generalPreferred:       generalPreferred,
		trustlinedEligible:     trustlinedEligible,
		active:                 make(map[int64]activeLease),
		availabilitySignalChan: make(chan struct{}, 1),
		recoveryQueue:          make(chan int, max(1, total)),
		recoveryRetryDelay:     time.Millisecond,
		reloadSequence:         reloadSequence,
	}
	if manager.reloadSequence == nil {
		manager.reloadSequence = func(_ context.Context, _ *keypair.Full) (int64, error) {
			return baseSequence, nil
		}
	}
	go manager.runRecoveryLoop()
	return manager, accounts
}

func TestAccountManagerPrefersGeneralOnlyAccounts(t *testing.T) {
	manager, accounts := newTestAccountManager(t, 2, 1, 99, nil)

	first, err := manager.Acquire(context.Background(), accountRequirementAnySource)
	require.NoError(t, err)
	second, err := manager.Acquire(context.Background(), accountRequirementAnySource)
	require.NoError(t, err)
	third, err := manager.Acquire(context.Background(), accountRequirementAnySource)
	require.NoError(t, err)

	require.Equal(t, accounts[0].Address(), first.Account.Address())
	require.Equal(t, accounts[1].Address(), second.Account.Address())
	require.Equal(t, accounts[2].Address(), third.Account.Address())
	require.Equal(t, int64(100), first.Sequence)
	require.Equal(t, int64(1), first.RequestID)
}

func TestAccountManagerTrustlinedLeaseRetriesSameSequence(t *testing.T) {
	manager, accounts := newTestAccountManager(t, 1, 1, 40, nil)

	lease, err := manager.Acquire(context.Background(), accountRequirementTrustlinedSource)
	require.NoError(t, err)
	require.Equal(t, accounts[1].Address(), lease.Account.Address())
	require.Equal(t, int64(41), lease.Sequence)

	manager.ReleaseRetryable(lease.RequestID)

	retried, err := manager.Acquire(context.Background(), accountRequirementTrustlinedSource)
	require.NoError(t, err)
	require.Equal(t, accounts[1].Address(), retried.Account.Address())
	require.Equal(t, int64(41), retried.Sequence)
	require.Greater(t, retried.RequestID, lease.RequestID)
	manager.ReleaseConsumed(retried.RequestID)
}

func TestAccountManagerConsumedLeaseAdvancesSequence(t *testing.T) {
	manager, accounts := newTestAccountManager(t, 1, 0, 7, nil)

	lease, err := manager.Acquire(context.Background(), accountRequirementAnySource)
	require.NoError(t, err)
	require.Equal(t, accounts[0].Address(), lease.Account.Address())
	require.Equal(t, int64(8), lease.Sequence)

	manager.ReleaseConsumed(lease.RequestID)

	next, err := manager.Acquire(context.Background(), accountRequirementAnySource)
	require.NoError(t, err)
	require.Equal(t, accounts[0].Address(), next.Account.Address())
	require.Equal(t, int64(9), next.Sequence)
}

func TestAccountManagerAmbiguousLeaseRecoversAccount(t *testing.T) {
	var recoveredAddress string
	manager, accounts := newTestAccountManager(t, 1, 1, 5, func(_ context.Context, kp *keypair.Full) (int64, error) {
		if kp.Address() == recoveredAddress {
			return 20, nil
		}
		return 30, nil
	})
	recoveredAddress = accounts[0].Address()

	first, err := manager.Acquire(context.Background(), accountRequirementAnySource)
	require.NoError(t, err)
	manager.ReleaseAmbiguous(first.RequestID)

	require.Eventually(t, func() bool {
		lease, err := manager.Acquire(context.Background(), accountRequirementAnySource)
		if err != nil {
			return false
		}
		if lease.Account.Address() != accounts[0].Address() {
			manager.ReleaseConsumed(lease.RequestID)
			return false
		}
		defer manager.ReleaseConsumed(lease.RequestID)
		return lease.Sequence == 21
	}, time.Second, 10*time.Millisecond)
}

func TestAccountManagerAmbiguousLeaseRetriesRecoveryUntilSuccess(t *testing.T) {
	var attempts atomic.Int64
	manager, accounts := newTestAccountManager(t, 1, 0, 9, func(_ context.Context, _ *keypair.Full) (int64, error) {
		attempt := attempts.Add(1)
		if attempt < 2 {
			return 0, context.DeadlineExceeded
		}
		return 15, nil
	})

	lease, err := manager.Acquire(context.Background(), accountRequirementAnySource)
	require.NoError(t, err)
	manager.ReleaseAmbiguous(lease.RequestID)

	require.Eventually(t, func() bool {
		next, err := manager.Acquire(context.Background(), accountRequirementAnySource)
		if err != nil {
			return false
		}
		defer manager.ReleaseConsumed(next.RequestID)
		return next.Account.Address() == accounts[0].Address() && next.Sequence == 16 && attempts.Load() >= 2
	}, time.Second, 10*time.Millisecond)
}
