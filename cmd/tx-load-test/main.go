// tx-load-test is a standalone load-testing tool that benchmarks a Stellar RPC
// endpoint through three distinct Soroban workloads:
//
//   - sac-transfer   SAC token transfers between random participant accounts.
//   - oz-transfer    OZ-style custom token transfers between random accounts.
//   - soroswap       Soroswap AMM swaps split 50/50 across two independent pools.
//
// Usage:
//
//	tx-load-test setup     --rpc-url <url> [--accounts N] [--state-file state.json]
//	tx-load-test bench     [--state-file state.json] --mode <mode> --target-rps N
//	tx-load-test teardown  [--state-file state.json]
//
// The three-phase design allows setup to be run once, benchmarks run many
// times, and teardown run once to clean up.
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/benchmark"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/setup"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/syncstate"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/teardown"
)

func main() {
	root := buildRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// forceExitOnSecondSignal spawns a goroutine that listens for a second
// SIGINT/SIGTERM after the first one has cancelled ctx. If received, it
// immediately exits the process so a stuck cleanup cannot block shutdown.
func forceExitOnSecondSignal(logger *log.Entry) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Warnf("received second signal (%s)  -- force exiting", sig)
		os.Exit(1)
	}()
}

// buildRootCmd assembles the cobra command tree.
func buildRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "tx-load-test",
		Short:         "Soroban RPC load-testing tool",
		SilenceErrors: true,
		SilenceUsage:  false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Usage()
		},
	}

	root.AddCommand(buildSetupCmd())
	root.AddCommand(buildBenchCmd())
	root.AddCommand(buildTeardownCmd())
	root.AddCommand(buildSyncCmd())
	return root
}

// ---------------------------------------------------------------------------
// Shared flags
// ---------------------------------------------------------------------------

// standaloneNetworkPassphrase is the well-known passphrase used by local
// Stellar quickstart / standalone deployments.
const standaloneNetworkPassphrase = "Standalone Network ; February 2017"

// knownNetworks maps short network names to their canonical Stellar network
// passphrases.
var knownNetworks = map[string]string{
	"testnet":    network.TestNetworkPassphrase,
	"futurenet":  network.FutureNetworkPassphrase,
	"mainnet":    network.PublicNetworkPassphrase,
	"standalone": standaloneNetworkPassphrase,
}

// addCommonFlags registers flags that are shared by setup. Bench and teardown
// use the state file instead of these flags.
func addCommonFlags(cmd *cobra.Command) {
	cmd.Flags().String("rpc-url", "", "Stellar RPC HTTP endpoint (required)")
	cmd.Flags().String("network", "testnet", `Network shorthand: testnet | futurenet | mainnet | standalone`)
	cmd.Flags().String("network-passphrase", "", "Override the network passphrase directly (takes precedence over --network)")
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	_ = cmd.MarkFlagRequired("rpc-url")
}

// makeLogger constructs a logger for the given service name, applying the
// --log-level flag from cmd.
func makeLogger(cmd *cobra.Command, service string) (*log.Entry, error) {
	levelStr, err := cmd.Flags().GetString("log-level")
	if err != nil {
		return nil, err
	}
	level, err := logrus.ParseLevel(levelStr)
	if err != nil {
		return nil, fmt.Errorf("invalid --log-level %q: must be debug, info, warn, or error", levelStr)
	}
	logger := log.New().WithField("service", service)
	logger.SetLevel(level)
	return logger, nil
}

// commonConfig extracts the shared flags from a command into a config.Config.
func commonConfig(cmd *cobra.Command, cfg *config.Config) error {
	var err error
	if cfg.RPCURL, err = cmd.Flags().GetString("rpc-url"); err != nil {
		return err
	}
	// Fee-payer seed comes exclusively from the environment; if absent the
	// setup step will generate and friendbot-fund a temporary keypair.
	cfg.FeePayerSeed = os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED")

	// --network-passphrase takes explicit precedence; fall back to --network.
	explicitPassphrase, err := cmd.Flags().GetString("network-passphrase")
	if err != nil {
		return err
	}
	if explicitPassphrase != "" {
		cfg.NetworkPassphrase = explicitPassphrase
	} else {
		networkName, nerr := cmd.Flags().GetString("network")
		if nerr != nil {
			return nerr
		}
		passphrase, ok := knownNetworks[networkName]
		if !ok {
			return fmt.Errorf("unknown network %q: must be one of testnet, futurenet, mainnet, standalone", networkName)
		}
		cfg.NetworkPassphrase = passphrase
	}
	return nil
}

// ---------------------------------------------------------------------------
// setup command
// ---------------------------------------------------------------------------

func buildSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Create all required ledger state for the benchmark",
		Long: `setup performs the full one-time initialization required before running
a benchmark:

  1. Verifies / funds the fee-payer account.
  2. Creates 3 classic benchmark assets (BLTA, BLTB, BLTC).
  3. Creates participant accounts with XLM balance and trustlines.
  4. Mints asset balances to every participant account.
  5. Deploys a SAC instance for each benchmark asset.

The resulting state is written to a JSON file (default: state.json) which
bench and teardown consume. Run setup once; run bench as many times as needed;
run teardown to clean up.

If a state.json already exists, setup will load it and only create the
additional accounts needed to reach the --accounts target. This lets you
expand an existing account pool without a full teardown/setup cycle.`,
		RunE: runSetup,
	}

	addCommonFlags(cmd)
	cmd.Flags().Int("accounts", 5_000, "Number of participant accounts to create")
	cmd.Flags().Float64("base-reserve-xlm", 3.0, "XLM to fund each account (0.5 base + 3x0.5 trustlines + 1.0 margin)")
	cmd.Flags().Int64("liquidity-per-pool", 1_000_000, "Token units to deposit into each Soroswap pool")
	return cmd
}

func runSetup(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/setup")
	if err != nil {
		return err
	}

	stateFile, err := cmd.Flags().GetString("state-file")
	if err != nil {
		return err
	}

	cfg := config.DefaultConfig()
	if err = commonConfig(cmd, &cfg); err != nil {
		return err
	}
	if cfg.NumberOfAccounts, err = cmd.Flags().GetInt("accounts"); err != nil {
		return err
	}
	if cfg.BaseReserveXLM, err = cmd.Flags().GetFloat64("base-reserve-xlm"); err != nil {
		return err
	}
	if cfg.LiquidityPerPool, err = cmd.Flags().GetInt64("liquidity-per-pool"); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	forceExitOnSecondSignal(logger)

	var st *state.State

	// If a state file already exists, load it so setup only does delta work
	// (e.g. creating additional accounts to reach the --accounts target).
	if _, statErr := os.Stat(stateFile); statErr == nil {
		ps, loadErr := state.NewPersistedState(stateFile)
		if loadErr != nil {
			return fmt.Errorf("state file %q exists but cannot be loaded: %w", stateFile, loadErr)
		}
		if cfg.FeePayerSeed == "" {
			return fmt.Errorf(
				"TX_LOAD_TEST_FEE_PAYER_SEED is required when re-running setup " +
					"with an existing state file")
		}
		st, err = state.FromPersistedState(ps, cfg.FeePayerSeed)
		if err != nil {
			return fmt.Errorf("reconstruct state from %q: %w", stateFile, err)
		}
		logger.Infof("loaded existing state from %s (%d accounts)", stateFile, len(st.AccountKPs))
	}

	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("panic during setup: %v  -- running best-effort cleanup", r)
			teardown.BestEffortCleanup(logger, cfg, st, stateFile)
			panic(r)
		}
		if ctx.Err() != nil && st != nil {
			logger.Info("setup interrupted  -- running best-effort cleanup")
			teardown.BestEffortCleanup(logger, cfg, st, stateFile)
		}
	}()

	st, err = setup.Setup(ctx, logger, cfg, st)
	if err != nil {
		logger.WithError(err).Error("setup failed")
		return err
	}

	// Write state to disk for bench / teardown.
	ps := st.ToPersistedState(cfg.RPCURL)
	if err := ps.Save(stateFile); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	logger.Infof("state written to %s", stateFile)
	return nil
}

// ---------------------------------------------------------------------------
// bench command
// ---------------------------------------------------------------------------

func buildBenchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Run a load-test benchmark against a pre-initialized ledger",
		Long: `bench runs one of three Soroban workloads against an already initialized
ledger (created by 'setup'):

  sac-transfer   -- SAC token transfers between random participant accounts.
  oz-transfer    -- OZ custom-token transfers between random accounts.
  soroswap       -- Soroswap swaps split 50/50 across pool-0 and pool-1.

The load ramps linearly from 1 RPS to --target-rps over --ramp-up, then
holds constant for the remainder of --duration (~100 s / ~20 ledgers).

bench reads the state file produced by setup and does not modify ledger state.
Run bench as many times as needed.`,
		RunE: runBench,
	}

	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	cmd.Flags().String("mode", string(config.ModeSACTransfer),
		fmt.Sprintf("Benchmark mode: %s | %s | %s",
			config.ModeSACTransfer, config.ModeOZTransfer, config.ModeSoroswap))
	cmd.Flags().Duration("duration", 100*time.Second, "Total benchmark duration")
	cmd.Flags().Duration("ramp-up", 20*time.Second, "Ramp-up period (RPS increases linearly from 1 to target-rps)")
	cmd.Flags().Int("target-rps", 50, "Steady-state requests per second after ramp-up")
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

	// Load state from disk.
	ps, err := state.NewPersistedState(stateFile)
	if err != nil {
		return fmt.Errorf("load state file %q: %w  -- run 'setup' first", stateFile, err)
	}
	feePayerSeed := os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED")
	if feePayerSeed == "" {
		return fmt.Errorf(
			"TX_LOAD_TEST_FEE_PAYER_SEED is required -- set it to the fee-payer secret key")
	}
	st, err := state.FromPersistedState(ps, feePayerSeed)
	if err != nil {
		return fmt.Errorf("reconstruct state: %w", err)
	}

	cfg := config.DefaultConfig()
	cfg.RPCURL = ps.RPCURL
	cfg.NetworkPassphrase = ps.NetworkPassphrase
	cfg.NumberOfAccounts = len(ps.AccountIndices)

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

	// Fail fast on obvious misconfig before starting the attack.
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
			MaxIdleConns:        cfg.TargetRPS * 4,
			MaxIdleConnsPerHost: cfg.TargetRPS * 4,
			MaxConnsPerHost:     cfg.TargetRPS * 4,
		},
	}
	if err = benchmark.Run(ctx, logger, cfg, st, httpClient); err != nil {
		logger.WithError(err).Error("benchmark failed")
	}
	return err
}

// ---------------------------------------------------------------------------
// teardown command
// ---------------------------------------------------------------------------

func buildTeardownCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Merge all participant accounts back into the fee payer",
		Long: `teardown reads the state file produced by setup, merges every participant
account back into the fee-payer account (recovering all reserved XLM), and
deletes the state file on success.

If teardown is interrupted or partially fails, the state file is updated to
reflect only the remaining accounts so a subsequent teardown can finish the job.`,
		RunE: runTeardown,
	}

	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	return cmd
}

func runTeardown(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/teardown")
	if err != nil {
		return err
	}

	stateFile, err := cmd.Flags().GetString("state-file")
	if err != nil {
		return err
	}

	ps, err := state.NewPersistedState(stateFile)
	if err != nil {
		return fmt.Errorf("load state file %q: %w  -- nothing to tear down", stateFile, err)
	}
	feePayerSeed := os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED")
	if feePayerSeed == "" {
		return fmt.Errorf(
			"TX_LOAD_TEST_FEE_PAYER_SEED is required -- set it to the fee-payer secret key")
	}
	st, err := state.FromPersistedState(ps, feePayerSeed)
	if err != nil {
		return fmt.Errorf("reconstruct state: %w", err)
	}

	cfg := config.DefaultConfig()
	cfg.RPCURL = ps.RPCURL
	cfg.NetworkPassphrase = ps.NetworkPassphrase
	cfg.NumberOfAccounts = len(ps.AccountIndices)

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	forceExitOnSecondSignal(logger)

	logger.Infof("loaded state from %s (%d accounts)", stateFile, cfg.NumberOfAccounts)
	return teardown.Teardown(ctx, logger, cfg, st, stateFile)
}

// ---------------------------------------------------------------------------
// sync command
// ---------------------------------------------------------------------------

func buildSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile the state file with on-chain account existence",
		Long: `sync checks which participant accounts in the state file still exist
on-chain and removes any that are missing. Use this when the state file gets
out of sync with the network (e.g. after a manual merge or a network reset).`,
		RunE: runSync,
	}

	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	return cmd
}

func runSync(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/sync")
	if err != nil {
		return err
	}

	stateFile, err := cmd.Flags().GetString("state-file")
	if err != nil {
		return err
	}

	ps, err := state.NewPersistedState(stateFile)
	if err != nil {
		return fmt.Errorf("load state file %q: %w", stateFile, err)
	}
	feePayerSeed := os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED")
	if feePayerSeed == "" {
		return fmt.Errorf(
			"TX_LOAD_TEST_FEE_PAYER_SEED is required -- set it to the fee-payer secret key")
	}
	st, err := state.FromPersistedState(ps, feePayerSeed)
	if err != nil {
		return fmt.Errorf("reconstruct state: %w", err)
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts)", stateFile, len(ps.AccountIndices))
	return syncstate.SyncState(ctx, logger, st, stateFile)
}
