package teardown

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func TestCleanupBatchesSplitsInput(t *testing.T) {
	accounts := make([]*keypair.Full, 25)
	batches := cleanupBatches(accounts, 12)
	require.Len(t, batches, 3)
	require.Len(t, batches[0], 12)
	require.Len(t, batches[1], 12)
	require.Len(t, batches[2], 1)
}

func TestMinSafeKeepIsPositionPastLastHolder(t *testing.T) {
	kps := make([]*keypair.Full, 6)
	for i := range kps {
		kp, err := keypair.Random()
		require.NoError(t, err)
		kps[i] = kp
	}

	// Holders are the first 3 (the usual prefix) -> safe keep is 3.
	st := &state.State{AccountKPs: kps, SACHolderKPs: kps[:3]}
	require.Equal(t, 3, minSafeKeep(st))

	// No holders -> any keep is safe.
	require.Equal(t, 0, minSafeKeep(&state.State{AccountKPs: kps}))

	// A holder sitting at position 4 (non-prefix) forces safe keep to 5,
	// proving the check is membership/position based, not a count.
	st2 := &state.State{AccountKPs: kps, SACHolderKPs: []*keypair.Full{kps[0], kps[4]}}
	require.Equal(t, 5, minSafeKeep(st2))
}

func TestAccountsNotInReturnsComplementByAddress(t *testing.T) {
	kps := make([]*keypair.Full, 4)
	for i := range kps {
		kp, err := keypair.Random()
		require.NoError(t, err)
		kps[i] = kp
	}
	missing := accountsNotIn(kps, []*keypair.Full{kps[1], kps[3]})
	require.Equal(t, []string{kps[0].Address(), kps[2].Address()},
		[]string{missing[0].Address(), missing[1].Address()})

	require.Empty(t, accountsNotIn(kps, kps))
	require.Len(t, accountsNotIn(kps, nil), 4)
}

func TestRemoveMergedAccountsPrunesParticipantsAndHolders(t *testing.T) {
	kp1, err := keypair.Random()
	require.NoError(t, err)
	kp2, err := keypair.Random()
	require.NoError(t, err)
	kp3, err := keypair.Random()
	require.NoError(t, err)
	kp4, err := keypair.Random()
	require.NoError(t, err)

	st := &state.State{
		AccountKPs:   []*keypair.Full{kp1, kp2, kp3, kp4},
		SACHolderKPs: []*keypair.Full{kp1, kp2, kp3},
	}

	removeMergedAccounts(st, []*keypair.Full{kp2, kp4})
	require.Equal(t, []string{kp1.Address(), kp3.Address()}, []string{st.AccountKPs[0].Address(), st.AccountKPs[1].Address()})
	require.Equal(t, []string{kp1.Address(), kp3.Address()}, []string{st.SACHolderKPs[0].Address(), st.SACHolderKPs[1].Address()})
}
