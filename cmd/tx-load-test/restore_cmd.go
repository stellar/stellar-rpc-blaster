package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/benchmark"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func buildRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore archived Soroban state needed by benchmark workloads",
		Long: `restore runs benchmark-shaped simulations ahead of a load test to find
archived Soroban state, submits RestoreFootprint transactions when needed, and
optionally verifies the selected workload footprints are live.

For soroswap, restore also runs a benchmark-footprint validation pass that
builds the same rewritten footprints used by benchmark submissions and compares
them with fresh simulation footprints.

This keeps simulation out of the per-request benchmark hot path. Use restore
when a state file has been idle long enough that benchmark contract data may
have archived. SAC restore probes selected holder accounts with transfer-shaped
calls so simulation can report archived SAC WASM/code, contract instance state,
and any account-specific SAC contract data. SAC participant balances are classic
trustlines, not Soroban archived entries.`,
		RunE: runRestore,
	}

	addRuntimeStatePreflightFlags(cmd)
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("rpc-url", "", "Override the RPC URL stored in the state JSON file")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	cmd.Flags().String("mode", benchmark.RestoreModeAll,
		fmt.Sprintf("Restore mode: %s | %s | %s | %s", benchmark.RestoreModeAll, config.ModeSACTransfer, config.ModeOZTransfer, config.ModeSoroswap))
	cmd.Flags().Bool("dry-run", false, "Only simulate and log what would need restore; do not submit RestoreFootprint transactions")
	cmd.Flags().Bool("verify", false, "After restoring, re-run selected restore probes and fail if any still require restore")
	cmd.Flags().Int("account-start", 0, "0-based offset into the selected participant account list")
	cmd.Flags().Int("account-limit", 0, "Maximum selected accounts per applicable mode; 0 means all remaining accounts")
	cmd.Flags().Int("progress-interval", 100, "Log restore progress every N probes; set negative to disable periodic progress")
	return cmd
}

func runRestore(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/restore")
	if err != nil {
		return err
	}

	mode, err := cmd.Flags().GetString("mode")
	if err != nil {
		return err
	}
	dryRun, err := cmd.Flags().GetBool("dry-run")
	if err != nil {
		return err
	}
	verify, err := cmd.Flags().GetBool("verify")
	if err != nil {
		return err
	}
	accountStart, err := cmd.Flags().GetInt("account-start")
	if err != nil {
		return err
	}
	accountLimit, err := cmd.Flags().GetInt("account-limit")
	if err != nil {
		return err
	}
	progressInterval, err := cmd.Flags().GetInt("progress-interval")
	if err != nil {
		return err
	}

	stateFile, loaded, err := loadRuntimeStateFromCommand(cmd, state.RuntimePhaseRestore)
	if err != nil {
		return err
	}

	ctx, cancel := signalCommandContext(cmd, logger, true)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts, rpc=%s)", stateFile, len(loaded.Persisted.AccountIndices), loaded.RPCURL)
	return benchmark.RestoreArchivedState(ctx, logger, loaded.Live, benchmark.RestoreOptions{
		Mode:             mode,
		DryRun:           dryRun,
		Verify:           verify,
		AccountStart:     accountStart,
		AccountLimit:     accountLimit,
		ProgressInterval: progressInterval,
	})
}
