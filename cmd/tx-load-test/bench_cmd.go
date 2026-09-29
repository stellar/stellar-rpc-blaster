package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/benchmark"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func buildBenchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Run a load-test benchmark against a pre-initialized ledger",
		Long: `bench runs the supported Soroban workload against an already initialized
ledger (created by 'setup'):

	sac-transfer   -- SAC token transfers between random participant accounts.
	oz-transfer    -- OpenZeppelin token transfers between random participant accounts.
	soroswap       -- router-based swaps across the benchmark Soroswap pools.

The load ramps linearly from 1 RPS to --target-rps over --ramp-up, then
the selected Soroban workload and the parallel simple-payment stream hold
constant for the remainder of --duration (~100 s / ~20 ledgers). The
simple-payment stream uses native XLM payments at 1 payment operation per
transaction, and --classic-rps is interpreted as transactions/sec.

Use --trace-file to capture every benchmark submit and poll request/response
as NDJSON for post-run inspection.

Benchmark summary metrics are also written as flattened NDJSON. By default the
metrics filename includes the start timestamp and selected mode; use
--metrics-file to choose a specific path.

bench reads the state file produced by setup and drives benchmark transaction
traffic against the target network without modifying local setup metadata.
Run bench as many times as needed.`,
		RunE: runBench,
	}

	addRuntimeStatePreflightFlags(cmd)
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("rpc-url", "", "Override the RPC URL stored in the state JSON file")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	cmd.Flags().String("mode", string(config.ModeSACTransfer),
		fmt.Sprintf("Benchmark mode: %s | %s | %s", config.ModeSACTransfer, config.ModeOZTransfer, config.ModeSoroswap))
	cmd.Flags().Duration("duration", 100*time.Second, "Total benchmark duration")
	cmd.Flags().Duration("ramp-up", 20*time.Second, "Ramp-up period (RPS increases linearly from 1 to target-rps)")
	cmd.Flags().Int("target-rps", 50, "Steady-state requests per second after ramp-up")
	cmd.Flags().Int("classic-rps", config.DefaultClassicRPS, "Steady-state simple-payment transactions per second after ramp-up (1 payment op per tx)")
	cmd.Flags().String("trace-file", "", "Optional newline-delimited JSON file that captures every benchmark submit and poll request/response")
	cmd.Flags().String("metrics-file", "", "Optional NDJSON file for benchmark metrics (default: timestamped filename containing the selected mode)")
	return cmd
}

func runBench(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/bench")
	if err != nil {
		return err
	}

	cfg := config.DefaultConfig()
	modeStr, err := cmd.Flags().GetString("mode")
	if err != nil {
		return err
	}
	cfg.Mode = config.BenchmarkMode(modeStr)

	if cfg.Duration, err = cmd.Flags().GetDuration("duration"); err != nil {
		return err
	}
	if cfg.RampUp, err = cmd.Flags().GetDuration("ramp-up"); err != nil {
		return err
	}
	if cfg.TargetRPS, err = cmd.Flags().GetInt("target-rps"); err != nil {
		return err
	}
	if cfg.ClassicRPS, err = cmd.Flags().GetInt("classic-rps"); err != nil {
		return err
	}
	if cfg.TraceFile, err = cmd.Flags().GetString("trace-file"); err != nil {
		return err
	}
	if cfg.MetricsFile, err = cmd.Flags().GetString("metrics-file"); err != nil {
		return err
	}
	if cfg.MetricsFile == "" {
		cfg.MetricsFile = benchmark.DefaultMetricsFileName(cfg.Mode, time.Now())
	}
	if err = benchmark.ValidateCLIConfig(cfg); err != nil {
		return err
	}

	stateFile, loaded, err := loadRuntimeStateFromCommand(cmd, state.RuntimePhaseBench)
	if err != nil {
		return err
	}
	cfg.RPCURL = loaded.RPCURL
	cfg.NetworkPassphrase = loaded.Persisted.NetworkPassphrase
	cfg.NumberOfAccounts = len(loaded.Persisted.AccountIndices)

	if err = benchmark.ValidateConfig(cfg); err != nil {
		return err
	}

	ctx, cancel := signalCommandContext(cmd, logger, true)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts, rpc=%s)", stateFile, cfg.NumberOfAccounts, cfg.RPCURL)
	if cfg.TraceFile != "" {
		logger.Infof("benchmark trace file enabled: %s", cfg.TraceFile)
	}
	logger.Infof("benchmark metrics NDJSON file: %s", cfg.MetricsFile)

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        max(1, (cfg.TargetRPS+state.SimplePaymentTransactionRate(cfg))*4),
			MaxIdleConnsPerHost: max(1, (cfg.TargetRPS+state.SimplePaymentTransactionRate(cfg))*4),
			MaxConnsPerHost:     max(1, (cfg.TargetRPS+state.SimplePaymentTransactionRate(cfg))*4),
		},
	}
	if err = benchmark.Run(ctx, logger, cfg, loaded.Live, httpClient); err != nil {
		logger.WithError(err).Error("benchmark failed")
	}
	return err
}
