package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/benchmark"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/setup"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/teardown"
)

func buildSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Create all required ledger state for the benchmark",
		Long: `setup performs the full one-time initialization required before running
	any benchmark mode:

  1. Verifies / funds the fee-payer account.
  2. Creates 3 classic benchmark assets (BLTA, BLTB, BLTC).
	3. Creates participant accounts with XLM balance.
		4. Adds trustlines and classic asset balances to the formula-derived holder subset needed for the superset of supported benchmark modes.
	5. Deploys a SAC instance for each benchmark asset.
		6. Resolves or bootstraps Soroswap core contracts, benchmark pools, and initial pool liquidity.
	6. Deploys the OZ token contract and reconciles OZ balances for all participant accounts.

The resulting state is written to a JSON file (default: state.json) which
bench and teardown consume. Run setup once; run bench as many times as needed;
run teardown to clean up.

If a state.json already exists, setup will load it and only create the
additional accounts needed to reach the --accounts target. This lets you
expand an existing account pool without a full teardown/setup cycle. Re-running
setup also reconciles the OZ token so accounts missing benchmark balances are
minted before the command exits.`,
		RunE: runSetup,
	}

	addCommonFlags(cmd)
	cmd.Flags().String("mode", string(config.ModeSACTransfer),
		fmt.Sprintf("Deprecated and ignored; setup now provisions the superset needed for %s, %s, and %s", config.ModeSACTransfer, config.ModeOZTransfer, config.ModeSoroswap))
	_ = cmd.Flags().MarkDeprecated("mode", "setup now provisions all benchmark modes; this flag is ignored")
	cmd.Flags().Duration("duration", 100*time.Second, "Planned benchmark duration used when sizing account partitions")
	cmd.Flags().Int("target-rps", 50, "Planned Soroban steady-state requests per second used when sizing account partitions")
	cmd.Flags().Int("classic-rps", config.DefaultClassicRPS, "Planned simple-payment steady-state operations per second used when sizing account partitions across all benchmark modes (must be a multiple of 100)")
	cmd.Flags().String("soroswap-factory", "", "Soroswap factory contract ID (required on testnet/mainnet; optional on standalone/futurenet, where setup can auto-deploy it)")
	cmd.Flags().String("soroswap-router", "", "Soroswap router contract ID (required on testnet/mainnet; optional on standalone/futurenet, where setup can auto-deploy it)")
	cmd.Flags().Int("accounts", 5_000, "Number of participant accounts to create")
	cmd.Flags().Float64("base-reserve-xlm", 3.0, "XLM to fund each account (covers reserves, holder trustlines, and fee headroom)")
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
	if cfg.SoroswapFactoryContract, err = cmd.Flags().GetString("soroswap-factory"); err != nil {
		return err
	}
	if cfg.SoroswapRouterContract, err = cmd.Flags().GetString("soroswap-router"); err != nil {
		return err
	}
	if cfg.Duration, err = cmd.Flags().GetDuration("duration"); err != nil {
		return err
	}
	if cfg.TargetRPS, err = cmd.Flags().GetInt("target-rps"); err != nil {
		return err
	}
	if cfg.ClassicRPS, err = cmd.Flags().GetInt("classic-rps"); err != nil {
		return err
	}
	if err = validateSoroswapSetupConfig(cfg); err != nil {
		return err
	}
	if err = benchmark.ValidateSetupConfig(cfg); err != nil {
		return fmt.Errorf("planned benchmark shape is invalid for all-mode setup: %w", err)
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	forceExitOnSecondSignal(logger)

	st, err := state.LoadExistingSetupState(ctx, stateFile, cfg.FeePayerSeed, cfg.RPCURL, cfg.NetworkPassphrase)
	if err != nil {
		return err
	}
	if st != nil {
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

	persistState := func(st *state.State) error {
		if st == nil || st.FeePayerKP == nil {
			return nil
		}
		ps, err := st.ToPersistedState(cfg.RPCURL)
		if err != nil {
			return fmt.Errorf("build persisted state: %w", err)
		}
		if err := ps.Save(stateFile); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		return nil
	}

	st, err = setup.Setup(ctx, logger, cfg, st, persistState)
	if err != nil {
		logger.WithError(err).Error("setup failed")
		return err
	}

	logger.Infof("state written to %s", stateFile)
	return nil
}
