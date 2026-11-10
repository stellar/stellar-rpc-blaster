package benchmark

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// An example pool shape: 4500 accounts and 900 holders. It is not the
// deployed state. A 5 minute run needs 15 accounts per rps, so this supports
// 300 rps in total and 60 rps of soroswap.
const (
	mainnetAccounts = 4500
	mainnetHolders  = 900
)

func suiteCfg(mode config.BenchmarkMode, rps, classicRPS int) config.Config {
	return config.Config{
		Mode:       mode,
		TargetRPS:  rps,
		ClassicRPS: classicRPS,
		Duration:   5 * time.Minute,
	}
}

func TestPoolShortfalls(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want []PoolShortfall
	}{
		{
			name: "default sac rate fits",
			cfg:  suiteCfg(config.ModeSACTransfer, 60, 100),
		},
		{
			name: "sac at the account pool's exact limit fits",
			cfg:  suiteCfg(config.ModeSACTransfer, 200, 100),
		},
		{
			name: "sac one rps past the account pool",
			cfg:  suiteCfg(config.ModeSACTransfer, 201, 100),
			want: []PoolShortfall{{Mode: config.ModeSACTransfer, Need: 4515, Have: mainnetAccounts}},
		},
		{
			// sac-transfer takes its sources from the participant pool. Only
			// the 2 transfer endpoints need trustlines, so a high rate never
			// causes a holder shortfall.
			name: "fast sac needs no more holders",
			cfg:  suiteCfg(config.ModeSACTransfer, 200, 0),
		},
		{
			name: "soroswap at the holder pool's exact limit fits",
			cfg:  suiteCfg(config.ModeSoroswap, 60, 100),
		},
		{
			name: "soroswap past the holder pool",
			cfg:  suiteCfg(config.ModeSoroswap, 100, 100),
			want: []PoolShortfall{{Mode: config.ModeSoroswap, Holders: true, Need: 1500, Have: mainnetHolders}},
		},
		{
			name: "soroswap past both pools reports both",
			cfg:  suiteCfg(config.ModeSoroswap, 250, 100),
			want: []PoolShortfall{
				{Mode: config.ModeSoroswap, Need: 5250, Have: mainnetAccounts},
				{Mode: config.ModeSoroswap, Holders: true, Need: 3750, Have: mainnetHolders},
			},
		},
		{
			name: "oz never needs holders",
			cfg:  suiteCfg(config.ModeOZTransfer, 200, 100),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, PoolShortfalls(tc.cfg, mainnetAccounts, mainnetHolders))
		})
	}
}

func TestValidatePoolCapacityAcceptsSupportedSuite(t *testing.T) {
	cfgs := []config.Config{
		suiteCfg(config.ModeSACTransfer, 60, 100),
		suiteCfg(config.ModeOZTransfer, 55, 100),
		suiteCfg(config.ModeSoroswap, 30, 100),
	}
	require.NoError(t, ValidatePoolCapacity(cfgs, mainnetAccounts, mainnetHolders))
}

func TestValidatePoolCapacityReportsEveryShortfallAndTheFix(t *testing.T) {
	cfgs := []config.Config{
		suiteCfg(config.ModeSACTransfer, 400, 100),
		suiteCfg(config.ModeOZTransfer, 55, 100),
		suiteCfg(config.ModeSoroswap, 100, 100),
	}
	err := ValidatePoolCapacity(cfgs, mainnetAccounts, mainnetHolders)
	require.Error(t, err)
	msg := err.Error()

	// One error reports both shortfalls, each with its rate.
	require.Contains(t, msg, "sac-transfer needs 7500 accounts; state has 4500 (400 rps + 100 classic rps for 5m0s)")
	require.Contains(t, msg, "soroswap needs 1500 trustlined holder accounts; state has 900 (100 rps + 100 classic rps for 5m0s)")
	require.NotContains(t, msg, "oz-transfer")

	// The fix follows the cause, as numbered steps. --accounts covers the
	// largest need. --target-rps comes from the soroswap rate, not the higher
	// sac rate.
	require.Contains(t, msg, "fix: lower the rates, or grow the pools:")
	require.Contains(t, msg, "1. tx-load-test setup --accounts 7500 --target-rps 100 --classic-rps 100 --duration 5m0s")
	require.Contains(t, msg, "2. redeploy the updated state file")
}

// With soroswap skipped, the suite needs 2 holders. The suggested setup must
// not size holders from the sac rate: at 400 rps that would create 6000
// holders that nothing uses.
func TestSetupSizingDoesNotGrowHoldersForHolderLightSuites(t *testing.T) {
	cfgs := []config.Config{suiteCfg(config.ModeSACTransfer, 400, 100)}
	err := ValidatePoolCapacity(cfgs, mainnetAccounts, mainnetHolders)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--accounts 7500 --target-rps 1 --classic-rps 100")

	// The suggested --target-rps creates no new holders.
	sizing := suiteCfg(config.ModeSoroswap, 1, 100)
	require.LessOrEqual(t, state.SorobanSourceAccountCount(sizing), mainnetHolders)
}

func TestSetupSizingNeverShrinksTheAccountTarget(t *testing.T) {
	// Only holders are short. --accounts must stay at the current 4500, not
	// drop to the 1500 this suite needs.
	cfgs := []config.Config{suiteCfg(config.ModeSoroswap, 100, 0)}
	err := ValidatePoolCapacity(cfgs, mainnetAccounts, mainnetHolders)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--accounts 4500 --target-rps 100 --classic-rps 0")
}

func TestProvisionedHolderCount(t *testing.T) {
	require.Zero(t, ProvisionedHolderCount(nil))

	kps := make([]*keypair.Full, 5)
	for i := range kps {
		kps[i] = keypair.MustRandom()
	}

	// The recorded holder subset is used when present.
	st := &state.State{AccountKPs: kps, SACHolderKPs: kps[:3]}
	require.Equal(t, 3, ProvisionedHolderCount(st))

	// Without a recorded subset, the default selection is used, as in the
	// benchmark.
	legacy := &state.State{AccountKPs: kps}
	require.Equal(t, len(state.DefaultSACHolderKPs(kps)), ProvisionedHolderCount(legacy))
}
