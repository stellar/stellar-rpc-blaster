package setup

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

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
