package benchmark

import (
	"context"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
	"github.com/stretchr/testify/require"
)

func testKeypairs(t *testing.T, n int) []*keypair.Full {
	t.Helper()
	accounts := make([]*keypair.Full, 0, n)
	for i := 0; i < n; i++ {
		accounts = append(accounts, keypair.MustRandom())
	}
	return accounts
}

func TestSACTransferVerifyReadyRequiresTrustlinedHolders(t *testing.T) {
	err := (sacTransferMode{}).VerifyReady(context.Background(), &state.State{AccountKPs: testKeypairs(t, 1)})
	require.EqualError(t, err, "SAC benchmark state incomplete: need at least 2 trustlined holder accounts, got 1 -- rerun setup")
}

func TestSACTransferNewTargeterRequiresContractIDs(t *testing.T) {
	accounts := testKeypairs(t, 2)
	_, err := (sacTransferMode{}).NewTargeter(context.Background(), "https://rpc.example", &state.State{
		AccountKPs:   accounts,
		SACHolderKPs: accounts,
	}, &fakeLeaseManager{eligibleAny: accounts, eligibleTrustlined: accounts})
	require.EqualError(t, err, "SAC benchmark state incomplete: SAC[0] contract ID is empty -- run setup first")
}

func TestOZTransferVerifyReadyRequiresParticipantAccounts(t *testing.T) {
	err := (ozTransferMode{}).VerifyReady(context.Background(), &state.State{})
	require.EqualError(t, err, "OZ benchmark state incomplete: need at least 2 participant accounts, got 0 -- rerun setup")
}

func TestOZTransferNewTargeterRequiresContractID(t *testing.T) {
	accounts := testKeypairs(t, 2)
	_, err := (ozTransferMode{}).NewTargeter(context.Background(), "https://rpc.example", &state.State{}, &fakeLeaseManager{eligibleAny: accounts, eligibleTrustlined: accounts})
	require.EqualError(t, err, "OZ benchmark state incomplete: OZ token contract ID is empty -- run setup first")
}

func TestSoroswapVerifyReadyRequiresFactoryContract(t *testing.T) {
	err := (soroswapMode{}).VerifyReady(context.Background(), &state.State{})
	require.EqualError(t, err, "Soroswap benchmark state incomplete: soroswap factory contract ID is empty -- run setup first")
}

func TestSoroswapNewTargeterRequiresParticipantAccounts(t *testing.T) {
	_, err := (soroswapMode{}).NewTargeter(context.Background(), "https://rpc.example", &state.State{}, &fakeLeaseManager{})
	require.EqualError(t, err, "Soroswap benchmark requires at least 1 participant account, got 0")
}
