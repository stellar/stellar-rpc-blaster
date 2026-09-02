package main

import (
	"github.com/spf13/cobra"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/extendttl"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func buildExtendTTLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extend-ttl",
		Short: "Extend the TTL of every Soroban entry the benchmark can touch",
		Long: `extend-ttl enumerates the complete Soroban footprint of a benchmark run --
contract instances (OZ token, SACs, soroswap router/factory/pairs), their Wasm
code entries, the pairs' SAC fund balances, and the OZ token balance of every
pool account -- and submits ExtendFootprintTtl transactions raising each
entry's TTL to --extend-to-days.

This exists because several of these entries are extended by nothing at
invocation time (the OZ instance and Wasm, and the SAC instances), so they
archive on a fixed calendar regardless of how often benches run; the rest are
only extended ~30 days per touch, so an idle gap longer than that archives
the whole hot set. Once archived, entries cannot be extended -- extend-ttl
reports them and fails unless --restore-archived is set, in which case it
first submits RestoreFootprint transactions bringing them back at the network
minimum persistent TTL (~120 days), then extends everything to the target.
(On protocol 23 this is the reliable way to heal archived state: the restore
subcommand's simulation probes classify every archived entry as
autorestore-class and submit nothing.)

The entry set is derived deterministically from the state file and the
FEE_PAYER-derived account pool; the chain is only consulted for current TTLs.
Entries already live past the target are skipped. At the default 180 days
(the network maximum) this is a roughly twice-a-year maintenance operation;
rent scales linearly with the extension length, so longer extensions cost the
same per ledger as shorter ones.`,
		RunE: runExtendTTL,
	}

	addRuntimeStatePreflightFlags(cmd)
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("rpc-url", "", "Override the RPC URL stored in the state JSON file")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	cmd.Flags().Float64("extend-to-days", 180,
		"Target TTL in days from now (clamped just below the network maxEntryTTL, ~180 days); entries already live past this are skipped")
	cmd.Flags().Int("batch-size", extendttl.DefaultBatchSize, "Footprint keys per ExtendFootprintTtl transaction")
	cmd.Flags().Bool("dry-run", false, "Classify and report entry TTLs and simulate every batch for a cost estimate; do not submit transactions")
	cmd.Flags().Bool("restore-archived", false,
		"Submit RestoreFootprint transactions for already-archived entries (back to the ~120-day network minimum TTL) before extending, instead of failing on them")
	cmd.Flags().Bool("skip-balances", false,
		"Extend only the infra set (instances, wasm, pair funds), excluding the ~per-account OZ balance entries; balances self-extend ~30 days on every bench touch, so include them only ahead of a planned idle gap longer than that")
	return cmd
}

func runExtendTTL(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/extend-ttl")
	if err != nil {
		return err
	}

	extendToDays, err := cmd.Flags().GetFloat64("extend-to-days")
	if err != nil {
		return err
	}
	batchSize, err := cmd.Flags().GetInt("batch-size")
	if err != nil {
		return err
	}
	dryRun, err := cmd.Flags().GetBool("dry-run")
	if err != nil {
		return err
	}
	skipBalances, err := cmd.Flags().GetBool("skip-balances")
	if err != nil {
		return err
	}
	restoreArchived, err := cmd.Flags().GetBool("restore-archived")
	if err != nil {
		return err
	}

	stateFile, loaded, err := loadRuntimeStateFromCommand(cmd, state.RuntimePhaseExtendTTL)
	if err != nil {
		return err
	}

	ctx, cancel := signalCommandContext(cmd, logger, true)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts, rpc=%s)", stateFile, len(loaded.Persisted.AccountIndices), loaded.RPCURL)
	return extendttl.Run(ctx, logger, loaded.Live, extendttl.Options{
		ExtendToLedgers: uint32(extendToDays * extendttl.LedgersPerDay),
		BatchSize:       batchSize,
		DryRun:          dryRun,
		SkipBalances:    skipBalances,
		RestoreArchived: restoreArchived,
	})
}
