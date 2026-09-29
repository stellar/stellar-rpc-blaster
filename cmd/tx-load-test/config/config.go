// Package config holds configuration for the tx-load-test tool.
package config

import "time"

// BenchmarkMode selects which workload to run during the benchmark phase.
type BenchmarkMode string

const (
	// ModeSACTransfer runs random SAC transfers between random accounts.
	ModeSACTransfer BenchmarkMode = "sac-transfer"
	// ModeOZTransfer runs random OpenZeppelin token transfers between accounts.
	ModeOZTransfer BenchmarkMode = "oz-transfer"
	// ModeSoroswap runs swaps against benchmark Soroswap pools.
	ModeSoroswap BenchmarkMode = "soroswap"
	// DefaultClassicRPS is the default steady-state transaction rate for the
	// parallel simple-payment stream that accompanies the selected Soroban
	// benchmark mode.
	DefaultClassicRPS = 200
)

// Config holds all parameters for setting up and running the tx-load-test.
type Config struct {
	// RPC endpoint to target.
	RPCURL string

	// Stellar network passphrase (e.g. "Test SDF Network ; September 2015").
	NetworkPassphrase string

	// FeePayerSeed is the secret key of the account that pays fees for all setup transactions.
	FeePayerSeed string

	// NumberOfAccounts is how many benchmark participant accounts to create.
	// Defaults to 5000.
	NumberOfAccounts int

	// BaseReserveXLM is the per-account funding amount in XLM.
	// 0.5 base reserve + 3 x 0.5 trustline entries = 2.0 XLM minimum; an extra
	// 1.0 XLM is added as a safety margin, giving a default of 3.0 XLM.
	BaseReserveXLM float64

	// LiquidityPerPool is the amount of each asset to deposit into each Soroswap pool.
	LiquidityPerPool int64

	// SoroswapFactoryContract is the strkey-encoded Soroswap factory contract ID
	// used when setting up benchmark pools.
	SoroswapFactoryContract string

	// SoroswapRouterContract is the strkey-encoded Soroswap router contract ID
	// used for benchmark swap traffic.
	SoroswapRouterContract string

	// Benchmark settings -------------------------------------------------------

	// Mode selects which workload to run.
	Mode BenchmarkMode

	// Duration is the total benchmark run time.
	// Defaults to 100 seconds (~20 ledgers).
	Duration time.Duration

	// RampUp is the period over which RPS increases linearly from 1 to TargetRPS.
	RampUp time.Duration

	// TargetRPS is the steady-state requests per second once the ramp-up is complete.
	TargetRPS int

	// ClassicRPS is the steady-state transactions per second for the parallel
	// simple-payment stream. Each transaction carries one payment op. A value of
	// 0 disables simple payments.
	ClassicRPS int

	// TraceFile optionally captures per-request benchmark submit/poll traces as
	// newline-delimited JSON for post-run inspection.
	TraceFile string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		NumberOfAccounts: 5_000,
		BaseReserveXLM:   3.0,
		LiquidityPerPool: 1_000_000,
		Duration:         100 * time.Second,
		RampUp:           20 * time.Second,
		TargetRPS:        50,
		ClassicRPS:       DefaultClassicRPS,
	}
}
