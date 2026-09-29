package state

import (
	"math"
	"time"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
)

const (
	// BenchmarkLedgerCloseSeconds is the assumed ledger-close interval used when
	// sizing source-account pools for sustained benchmarks.
	BenchmarkLedgerCloseSeconds = 5
	// SimplePaymentOpsPerTransaction is the fixed number of payment operations
	// bundled into each classic simple-payment transaction.
	SimplePaymentOpsPerTransaction = 100
	// RecommendedSourceReuseLedgers is the target minimum source-account reuse
	// interval used by the recommended pool-size formula.
	RecommendedSourceReuseLedgers = 2
	// SourceAccountRunBudget is the maximum number of transactions one source
	// account should ideally submit over a single benchmark run before we size a
	// larger source-account pool.
	SourceAccountRunBudget = 12
)

func sourceAccountsForRun(rps int, duration time.Duration) int {
	if rps <= 0 {
		return 0
	}
	concurrency := int(math.Ceil(float64(rps*BenchmarkLedgerCloseSeconds))) * RecommendedSourceReuseLedgers
	budget := 0
	if duration > 0 {
		budget = int(math.Ceil(float64(rps) * duration.Seconds() / SourceAccountRunBudget))
	}
	return max(concurrency, budget)
}

// SorobanSourceAccountCount returns the source-account requirement for the
// selected Soroban benchmark stream.
func SorobanSourceAccountCount(cfg config.Config) int {
	return sourceAccountsForRun(cfg.TargetRPS, cfg.Duration)
}

// SimplePaymentTransactionRate returns the required simple-payment transaction
// rate, derived from the configured operations/sec and fixed batch size.
func SimplePaymentTransactionRate(cfg config.Config) int {
	if cfg.ClassicRPS <= 0 {
		return 0
	}
	return cfg.ClassicRPS / SimplePaymentOpsPerTransaction
}

// SimplePaymentSourceAccountCount returns the source-account requirement for
// the parallel simple-payment stream.
func SimplePaymentSourceAccountCount(cfg config.Config) int {
	return sourceAccountsForRun(SimplePaymentTransactionRate(cfg), cfg.Duration)
}

// HolderAccountCount returns the number of trustlined / asset-funded accounts
// required by the configured benchmark shape.
func HolderAccountCount(cfg config.Config) int {
	if cfg.Mode == config.ModeSACTransfer {
		return 2
	}
	return 0
}

// RecommendedHolderAccountCount returns the formula-derived target holder count
// capped by the total number of participant accounts available.
func RecommendedHolderAccountCount(cfg config.Config, totalAccounts int) int {
	if totalAccounts <= 0 {
		return 0
	}
	return min(totalAccounts, HolderAccountCount(cfg))
}
