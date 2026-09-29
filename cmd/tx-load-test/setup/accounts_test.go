package setup

import (
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func TestBuildAccountProvisionPlan(t *testing.T) {
	feePayer, err := keypair.Random()
	require.NoError(t, err)

	existing := make([]*keypair.Full, 0, 5)
	for idx := 1; idx <= 5; idx++ {
		kp, err := state.DeriveKeypair(feePayer, idx)
		require.NoError(t, err)
		existing = append(existing, kp)
	}

	st := &state.State{
		FeePayerKP:   feePayer,
		AccountKPs:   existing,
		SACHolderKPs: existing[:2],
	}
	cfg := config.DefaultConfig()
	cfg.NumberOfAccounts = 8
	cfg.TargetRPS = 1
	cfg.Duration = time.Second

	plan, err := buildAccountProvisionPlan(cfg, st)
	require.NoError(t, err)
	require.Equal(t, 5, plan.existingCount)
	require.Equal(t, 8, plan.targetCount)
	require.Equal(t, 2, plan.existingHolders)
	require.Equal(t, 8, plan.targetHolders)
	require.Len(t, plan.promotedExistingKPs, 3)
	require.Equal(t, existing[2].Address(), plan.promotedExistingKPs[0].Address())
	require.Len(t, plan.newKPs, 3)
	require.Len(t, plan.holderNewKPs, 3)
	require.Empty(t, plan.passiveNewKPs)
	for i, kp := range plan.newKPs {
		idx, err := state.RecoverIndex(kp)
		require.NoError(t, err)
		require.Equal(t, 6+i, idx)
	}
}

func TestBuildAccountProvisionPlanPromotesExistingWithoutAppend(t *testing.T) {
	feePayer, err := keypair.Random()
	require.NoError(t, err)

	existing := make([]*keypair.Full, 0, 10)
	for idx := 1; idx <= 10; idx++ {
		kp, err := state.DeriveKeypair(feePayer, idx)
		require.NoError(t, err)
		existing = append(existing, kp)
	}

	st := &state.State{
		FeePayerKP:   feePayer,
		AccountKPs:   existing,
		SACHolderKPs: existing[:2],
	}
	cfg := config.DefaultConfig()
	cfg.NumberOfAccounts = 10
	cfg.Mode = config.ModeSoroswap
	cfg.TargetRPS = 1
	cfg.Duration = time.Second

	plan, err := buildAccountProvisionPlan(cfg, st)
	require.NoError(t, err)
	require.Equal(t, 10, plan.existingCount)
	require.Equal(t, 10, plan.targetCount)
	require.Equal(t, 2, plan.existingHolders)
	require.Equal(t, 10, plan.targetHolders)
	require.Len(t, plan.promotedExistingKPs, 8)
	require.Empty(t, plan.newKPs)
	require.Empty(t, plan.holderNewKPs)
	require.Empty(t, plan.passiveNewKPs)
	require.Equal(t, existing[2].Address(), plan.promotedExistingKPs[0].Address())
	require.Equal(t, existing[9].Address(), plan.promotedExistingKPs[7].Address())
}

func TestMinimumFeePayerBalanceUsesAccountDelta(t *testing.T) {
	feePayer, err := keypair.Random()
	require.NoError(t, err)

	existing := make([]*keypair.Full, 0, 5)
	for idx := 1; idx <= 5; idx++ {
		kp, err := state.DeriveKeypair(feePayer, idx)
		require.NoError(t, err)
		existing = append(existing, kp)
	}

	cfg := config.DefaultConfig()
	cfg.NumberOfAccounts = 5
	cfg.BaseReserveXLM = 3

	require.Equal(t, 115.0, minimumFeePayerBalanceXLM(cfg, nil))
	require.Equal(t, 106.0, minimumFeePayerBalanceXLM(cfg, &state.State{AccountKPs: existing[:3]}))
	require.Equal(t, 100.0, minimumFeePayerBalanceXLM(cfg, &state.State{AccountKPs: existing}))

	cfg.NumberOfAccounts = 2
	require.Equal(t, 100.0, minimumFeePayerBalanceXLM(cfg, &state.State{AccountKPs: existing}))
}

func TestPlanSACHolderRepairs(t *testing.T) {
	issuer, err := keypair.Random()
	require.NoError(t, err)
	accountA, err := keypair.Random()
	require.NoError(t, err)
	accountB, err := keypair.Random()
	require.NoError(t, err)

	st := &state.State{
		Assets: [3]txnbuild.CreditAsset{
			{Code: "BLTA", Issuer: issuer.Address()},
			{Code: "BLTB", Issuer: issuer.Address()},
			{Code: "BLTC", Issuer: issuer.Address()},
		},
	}

	balances := map[string]map[string]xdr.Int64{
		accountA.Address(): {
			"BLTA": 100,
			"BLTB": 0,
		},
		accountB.Address(): {
			"BLTA": 100,
			"BLTB": 100,
			"BLTC": 100,
		},
	}

	trustlineRepairKPs, mintPlan := planSACHolderRepairs(st, []*keypair.Full{accountA, accountB}, balances)

	require.Len(t, trustlineRepairKPs, 1)
	require.Equal(t, accountA.Address(), trustlineRepairKPs[0].Address())
	require.Len(t, mintPlan, 1)
	require.Len(t, mintPlan[accountA.Address()], 2)
	require.Equal(t, "BLTB", mintPlan[accountA.Address()][0].GetCode())
	require.Equal(t, "BLTC", mintPlan[accountA.Address()][1].GetCode())
	_, ok := mintPlan[accountB.Address()]
	require.False(t, ok)
}

func TestPassiveParticipantAccounts(t *testing.T) {
	a, err := keypair.Random()
	require.NoError(t, err)
	b, err := keypair.Random()
	require.NoError(t, err)
	c, err := keypair.Random()
	require.NoError(t, err)

	accounts := []*keypair.Full{a, b, c}
	require.Equal(t, []*keypair.Full{b, c}, passiveParticipantAccounts(accounts, 1))
	require.Nil(t, passiveParticipantAccounts(accounts, len(accounts)))
	require.Equal(t, accounts, passiveParticipantAccounts(accounts, -1))
}
