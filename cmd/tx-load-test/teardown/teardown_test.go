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
