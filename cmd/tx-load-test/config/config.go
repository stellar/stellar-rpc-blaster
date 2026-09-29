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
	}
}
