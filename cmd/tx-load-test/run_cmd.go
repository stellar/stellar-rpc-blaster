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

// suiteModeOrder is the fixed execution order of the benchmark suite. The
// modes share the derived account pool, so they must run sequentially; each
// benchmark.Run reloads sequence numbers from the RPC, which makes
// back-to-back runs against the same loaded state safe.
var suiteModeOrder = []config.BenchmarkMode{
	config.ModeSACTransfer,
	config.ModeOZTransfer,
	config.ModeSoroswap,
}

func buildRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the full benchmark suite (sac-transfer, oz-transfer, soroswap) sequentially",
		Long: `run executes every benchmark mode back to back against an already
initialized ledger (created by 'setup'): sac-transfer, then oz-transfer, then
soroswap, each for --duration at its own per-mode rate, with the parallel
simple-payment stream at --classic-rps alongside each.

The modes share the derived account pool so they never run concurrently; a
--pause settle gap separates them. Each mode writes its own timestamped
metrics NDJSON file under metrics/ (as if 'bench' had been run per mode) and,
when --metrics-gcs-url is set, uploads it right after the mode finishes so an
upload or later-mode failure cannot lose earlier results. A failed mode aborts
the remaining ones.

This exists so scheduled runs (e.g. a Kubernetes CronJob) need a single
invocation for the whole suite; use 'bench' for a one-off single-mode run.`,
		RunE: runSuite,
	}

	addRuntimeStatePreflightFlags(cmd)
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("rpc-url", "", "Override the RPC URL stored in the state JSON file")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	cmd.Flags().Duration("duration", 5*time.Minute, "Benchmark duration for each mode")
	cmd.Flags().Duration("ramp-up", 0, "Ramp-up period for each mode (RPS increases linearly from 1 to the mode's rate)")
	cmd.Flags().Duration("pause", 30*time.Second, "Settle gap between modes")
	cmd.Flags().Int("sac-rps", 60, "Steady-state requests per second for sac-transfer")
	cmd.Flags().Int("oz-rps", 55, "Steady-state requests per second for oz-transfer")
	cmd.Flags().Int("soroswap-rps", 30, "Steady-state requests per second for soroswap")
	cmd.Flags().Int("classic-rps", config.DefaultClassicRPS, "Steady-state simple-payment transactions per second alongside each mode (1 payment op per tx)")
	cmd.Flags().String("metrics-gcs-url", "", "Optional gs://bucket/prefix/ destination; each mode's metrics file is uploaded there after the mode finishes (auth via Application Default Credentials)")
	return cmd
}

func runSuite(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/run")
	if err != nil {
		return err
	}

	duration, err := cmd.Flags().GetDuration("duration")
	if err != nil {
		return err
	}
	rampUp, err := cmd.Flags().GetDuration("ramp-up")
	if err != nil {
		return err
	}
	pause, err := cmd.Flags().GetDuration("pause")
	if err != nil {
		return err
	}
	classicRPS, err := cmd.Flags().GetInt("classic-rps")
	if err != nil {
		return err
	}
	metricsGCSURL, err := cmd.Flags().GetString("metrics-gcs-url")
	if err != nil {
		return err
	}
	modeRPS := map[config.BenchmarkMode]string{
		config.ModeSACTransfer: "sac-rps",
		config.ModeOZTransfer:  "oz-rps",
		config.ModeSoroswap:    "soroswap-rps",
	}

	cfgs := make([]config.Config, 0, len(suiteModeOrder))
	for _, mode := range suiteModeOrder {
		targetRPS, err := cmd.Flags().GetInt(modeRPS[mode])
		if err != nil {
			return err
		}
		cfg := config.DefaultConfig()
		cfg.Mode = mode
		cfg.Duration = duration
		cfg.RampUp = rampUp
		cfg.TargetRPS = targetRPS
		cfg.ClassicRPS = classicRPS
		if err := benchmark.ValidateCLIConfig(cfg); err != nil {
			return fmt.Errorf("%s: %w", mode, err)
		}
		cfgs = append(cfgs, cfg)
	}

	stateFile, loaded, err := loadRuntimeStateFromCommand(cmd, state.RuntimePhaseBench)
	if err != nil {
		return err
	}
	// Validate every mode before the first one starts, so a config/pool
	// problem in a later mode fails immediately instead of after most of the
	// suite has already run.
	for i := range cfgs {
		cfgs[i].RPCURL = loaded.RPCURL
		cfgs[i].NetworkPassphrase = loaded.Persisted.NetworkPassphrase
		cfgs[i].NumberOfAccounts = len(loaded.Persisted.AccountIndices)
		if err := benchmark.ValidateConfig(cfgs[i]); err != nil {
			return fmt.Errorf("%s: %w", cfgs[i].Mode, err)
		}
	}

	ctx, cancel := signalCommandContext(cmd, logger, true)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts, rpc=%s)", stateFile, cfgs[0].NumberOfAccounts, loaded.RPCURL)

	for i, cfg := range cfgs {
		if i > 0 && pause > 0 {
			logger.Infof("pausing %s before %s", pause, cfg.Mode)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pause):
			}
		}

		cfg.MetricsFile = benchmark.DefaultMetricsFileName(cfg.Mode, time.Now())
		scoped := logger.WithField("mode", string(cfg.Mode))
		scoped.Infof("benchmark metrics NDJSON file: %s", cfg.MetricsFile)

		connCap := max(1, (cfg.TargetRPS+state.SimplePaymentTransactionRate(cfg))*4)
		httpClient := &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        connCap,
				MaxIdleConnsPerHost: connCap,
				MaxConnsPerHost:     connCap,
			},
		}

		runErr := benchmark.Run(ctx, scoped, cfg, loaded.Live, httpClient)
		if runErr != nil {
			scoped.WithError(runErr).Error("benchmark failed")
		}
		if uploadErr := uploadMetricsIfRequested(scoped, metricsGCSURL, cfg.MetricsFile); uploadErr != nil && runErr == nil {
			runErr = uploadErr
		}
		if runErr != nil {
			return fmt.Errorf("%s: %w", cfg.Mode, runErr)
		}
	}

	logger.Infof("benchmark suite complete (%d modes)", len(cfgs))
	return nil
}
