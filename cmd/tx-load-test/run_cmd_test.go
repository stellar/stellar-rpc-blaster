package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
)

func suiteRates(sac, oz, soroswap int) map[config.BenchmarkMode]int {
	return map[config.BenchmarkMode]int{
		config.ModeSACTransfer: sac,
		config.ModeOZTransfer:  oz,
		config.ModeSoroswap:    soroswap,
	}
}

func modesOf(cfgs []config.Config) []config.BenchmarkMode {
	modes := make([]config.BenchmarkMode, len(cfgs))
	for i, cfg := range cfgs {
		modes[i] = cfg.Mode
	}
	return modes
}

func TestPlanSuiteAppliesPerModeRatesInOrder(t *testing.T) {
	cfgs, skipped, err := planSuite(suiteRates(200, 180, 50), 5*time.Minute, 30*time.Second, 100)
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.Equal(t,
		[]config.BenchmarkMode{config.ModeSACTransfer, config.ModeOZTransfer, config.ModeSoroswap},
		modesOf(cfgs))

	require.Equal(t, 200, cfgs[0].TargetRPS)
	require.Equal(t, 180, cfgs[1].TargetRPS)
	require.Equal(t, 50, cfgs[2].TargetRPS)
	for _, cfg := range cfgs {
		require.Equal(t, 5*time.Minute, cfg.Duration)
		require.Equal(t, 30*time.Second, cfg.RampUp)
		require.Equal(t, 100, cfg.ClassicRPS)
	}
}

func TestPlanSuiteSkipsZeroRateModes(t *testing.T) {
	cfgs, skipped, err := planSuite(suiteRates(200, 0, 0), 5*time.Minute, 0, 100)
	require.NoError(t, err)
	require.Equal(t, []config.BenchmarkMode{config.ModeSACTransfer}, modesOf(cfgs))
	require.Equal(t, []config.BenchmarkMode{config.ModeOZTransfer, config.ModeSoroswap}, skipped)

	// Skipping the first mode keeps the rest in suite order.
	cfgs, skipped, err = planSuite(suiteRates(0, 55, 30), 5*time.Minute, 0, 100)
	require.NoError(t, err)
	require.Equal(t, []config.BenchmarkMode{config.ModeOZTransfer, config.ModeSoroswap}, modesOf(cfgs))
	require.Equal(t, []config.BenchmarkMode{config.ModeSACTransfer}, skipped)
}

func TestPlanSuiteRejectsNegativeRates(t *testing.T) {
	_, _, err := planSuite(suiteRates(60, -1, 30), 5*time.Minute, 0, 100)
	require.EqualError(t, err, "--oz-rps must be >= 0 (0 skips oz-transfer)")
}

func TestPlanSuiteRejectsAllModesSkipped(t *testing.T) {
	_, _, err := planSuite(suiteRates(0, 0, 0), 5*time.Minute, 0, 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "every mode is skipped")
}

func TestPlanSuiteRejectsNegativeClassicRate(t *testing.T) {
	_, _, err := planSuite(suiteRates(60, 55, 30), 5*time.Minute, 0, -5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "classic-rps must be >= 0")
}

func TestDescribeSuiteMode(t *testing.T) {
	cfg := config.Config{
		Mode:       config.ModeSACTransfer,
		TargetRPS:  200,
		ClassicRPS: 100,
		Duration:   5 * time.Minute,
	}
	require.Equal(t,
		"sac-transfer at 200 rps (~1000 tx/ledger) + classic at 100 rps (~500 tx/ledger) for 5m0s",
		describeSuiteMode(cfg))

	cfg.ClassicRPS = 0
	require.Equal(t, "sac-transfer at 200 rps (~1000 tx/ledger) for 5m0s", describeSuiteMode(cfg))
}

// Every suite mode needs a rate flag on the command, with the 0-skips hint in
// its usage. The CronJob manifest sets each rate through these flags.
func TestRunCommandExposesARateFlagPerSuiteMode(t *testing.T) {
	cmd := buildRunCmd()
	for _, mode := range suiteModeOrder {
		name, ok := suiteModeRateFlags[mode]
		require.True(t, ok, "no rate flag registered for %s", mode)
		flag := cmd.Flags().Lookup(name)
		require.NotNil(t, flag, "flag --%s is not defined", name)
		require.Contains(t, flag.Usage, "0 skips the mode")
	}
	require.Len(t, suiteModeRateFlags, len(suiteModeOrder))
}
