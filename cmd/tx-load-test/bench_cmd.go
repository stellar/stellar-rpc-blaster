package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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

The load ramps linearly from 1 RPS to --target-rps over --ramp-up, then
the selected Soroban workload and the parallel simple-payment stream hold
constant for the remainder of --duration (~100 s / ~20 ledgers). The
simple-payment stream uses native XLM payments batched at 100 operations per
transaction, and --classic-rps is interpreted as operations/sec.

bench reads the state file produced by setup and does not modify ledger state.
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
	cmd.Flags().Int("classic-rps", config.DefaultClassicRPS, "Steady-state simple-payment operations per second after ramp-up (must be a multiple of 100)")
	return cmd
}

func runBench(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/bench")
	if err != nil {
		return err
	}

	stateFile, err := cmd.Flags().GetString("state-file")
	if err != nil {
		return err
	}
	rpcURL, err := cmd.Flags().GetString("rpc-url")
	if err != nil {
		return err
	}
	skipAccountPreflight, err := cmd.Flags().GetBool("skip-account-preflight")
	if err != nil {
		return err
	}
	accountPreflightSample, err := cmd.Flags().GetInt("account-preflight-sample")
	if err != nil {
		return err
	}

	loaded, err := state.LoadRuntimeStateWithOptions(cmd.Context(), state.RuntimePhaseBench, stateFile, os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED"), rpcURL, state.RuntimeLoadOptions{
		VerifyAccountsExist: !skipAccountPreflight,
		AccountCheckSample:  accountPreflightSample,
	})
	if err != nil {
		return err
	}

	cfg := config.DefaultConfig()
	cfg.RPCURL = loaded.RPCURL
	cfg.NetworkPassphrase = loaded.Persisted.NetworkPassphrase
	cfg.NumberOfAccounts = len(loaded.Persisted.AccountIndices)

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

	if err = benchmark.ValidateConfig(cfg); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	forceExitOnSecondSignal(logger)

	logger.Infof("loaded state from %s (%d accounts, rpc=%s)", stateFile, cfg.NumberOfAccounts, cfg.RPCURL)

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
