package benchmark

import (
	"fmt"
	"strings"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// PoolShortfall is one account pool that is too small for a benchmark mode.
type PoolShortfall struct {
	Mode config.BenchmarkMode
	// Holders is true for the trustlined holder subset, false for the
	// participant pool.
	Holders bool
	Need    int
	Have    int
}

func (s PoolShortfall) String() string {
	if s.Holders {
		return fmt.Sprintf("%s needs %d trustlined holder accounts; state has %d", s.Mode, s.Need, s.Have)
	}
	return fmt.Sprintf("%s needs %d accounts; state has %d", s.Mode, s.Need, s.Have)
}

// ProvisionedHolderCount returns the number of trustlined holder accounts in
// st. A state file without a recorded holder subset gets the same default
// selection the benchmark uses.
func ProvisionedHolderCount(st *state.State) int {
	if st == nil {
		return 0
	}
	if n := len(st.SACHolderKPs); n > 0 {
		return n
	}
	return len(state.DefaultSACHolderKPs(st.AccountKPs))
}

// PoolShortfalls returns every pool that is too small for cfg.
//
// It checks two pools:
//   - accounts: transaction sources for every workload, including the
//     classic stream.
//   - holders: soroswap traders, which need trustlines.
func PoolShortfalls(cfg config.Config, accounts, holders int) []PoolShortfall {
	var out []PoolShortfall
	if need := state.AnySourceAccountCount(cfg); accounts < need {
		out = append(out, PoolShortfall{Mode: cfg.Mode, Need: need, Have: accounts})
	}
	if need := state.HolderAccountCount(cfg); holders < need {
		out = append(out, PoolShortfall{Mode: cfg.Mode, Holders: true, Need: need, Have: holders})
	}
	return out
}

// ValidatePoolCapacity returns an error if the pools cannot sustain every
// config in cfgs. The error lists every shortfall and the setup command that
// fixes all of them.
//
// Call it before generating load. Without it, an undersized pool fails
// partway through a suite, or throttles the run and reports low throughput.
func ValidatePoolCapacity(cfgs []config.Config, accounts, holders int) error {
	var shortfalls []PoolShortfall
	for _, cfg := range cfgs {
		shortfalls = append(shortfalls, PoolShortfalls(cfg, accounts, holders)...)
	}
	if len(shortfalls) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("account pools are too small for the configured rates:\n")
	for _, s := range shortfalls {
		rate := rateSummaryFor(cfgs, s.Mode)
		fmt.Fprintf(&b, "  - %s (%s)\n", s, rate)
	}
	b.WriteString("fix: lower the rates, or grow the pools:\n")
	fmt.Fprintf(&b, "  1. tx-load-test setup %s <your usual --network/--rpc-url/--state-file/--soroswap-* flags>\n", setupSizingFlags(cfgs, accounts))
	b.WriteString("  2. redeploy the updated state file\n")
	b.WriteString("setup only creates the missing accounts and trustlines.")
	return fmt.Errorf("%s", b.String())
}

// rateSummaryFor returns mode's configured rates, formatted for an error.
func rateSummaryFor(cfgs []config.Config, mode config.BenchmarkMode) string {
	for _, cfg := range cfgs {
		if cfg.Mode != mode {
			continue
		}
		if classic := state.SimplePaymentTransactionRate(cfg); classic > 0 {
			return fmt.Sprintf("%d rps + %d classic rps for %s", cfg.TargetRPS, classic, cfg.Duration)
		}
		return fmt.Sprintf("%d rps for %s", cfg.TargetRPS, cfg.Duration)
	}
	return "unknown rate"
}

// setupSizingFlags returns the setup flags that make the pools large enough
// for every config in cfgs.
//
// --accounts is the total account target. --target-rps sets the holder
// target, so it is the smallest rate whose holder count covers the need. It
// is not the fastest rate in the suite: sac-transfer needs 2 holders at any
// rate, and each unneeded holder locks 1.5 XLM in trustline reserves.
func setupSizingFlags(cfgs []config.Config, accounts int) string {
	if len(cfgs) == 0 {
		return ""
	}
	accountTarget := accounts
	holderNeed := 0
	for _, cfg := range cfgs {
		accountTarget = max(accountTarget, state.AnySourceAccountCount(cfg))
		holderNeed = max(holderNeed, state.HolderAccountCount(cfg))
	}

	sizing := cfgs[0]
	sizing.TargetRPS = 1
	for state.SorobanSourceAccountCount(sizing) < holderNeed {
		sizing.TargetRPS++
	}
	return fmt.Sprintf("--accounts %d --target-rps %d --classic-rps %d --duration %s",
		accountTarget, sizing.TargetRPS, max(sizing.ClassicRPS, 0), sizing.Duration)
}
