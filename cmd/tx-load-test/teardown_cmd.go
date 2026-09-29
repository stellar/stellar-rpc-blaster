package main

import (
	"github.com/spf13/cobra"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/teardown"
)

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

	addRuntimeStatePreflightFlags(cmd)
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("rpc-url", "", "Override the RPC URL stored in the state JSON file")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	return cmd
}

func runTeardown(cmd *cobra.Command, _ []string) error {
	logger, err := makeLogger(cmd, "tx-load-test/teardown")
	if err != nil {
		return err
	}

	stateFile, loaded, err := loadRuntimeStateFromCommand(cmd, state.RuntimePhaseTeardown)
	if err != nil {
		return err
	}

	cfg := config.DefaultConfig()
	cfg.RPCURL = loaded.RPCURL
	cfg.NetworkPassphrase = loaded.Persisted.NetworkPassphrase
	cfg.NumberOfAccounts = len(loaded.Persisted.AccountIndices)

	ctx, cancel := signalCommandContext(cmd, logger, true)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts)", stateFile, cfg.NumberOfAccounts)
	return teardown.Teardown(ctx, logger, cfg, loaded.Live, stateFile)
}
