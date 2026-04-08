package main

import (
	"os"
	"os/signal"
	"syscall"

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

	stateFile, err := cmd.Flags().GetString("state-file")
	if err != nil {
		return err
	}
	rpcURL, err := cmd.Flags().GetString("rpc-url")
	if err != nil {
		return err
	}

	loaded, err := state.LoadRuntimeState(cmd.Context(), state.RuntimePhaseTeardown, stateFile, os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED"), rpcURL)
	if err != nil {
		return err
	}

	cfg := config.DefaultConfig()
	cfg.RPCURL = loaded.RPCURL
	cfg.NetworkPassphrase = loaded.Persisted.NetworkPassphrase
	cfg.NumberOfAccounts = len(loaded.Persisted.AccountIndices)

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	forceExitOnSecondSignal(logger)

	logger.Infof("loaded state from %s (%d accounts)", stateFile, cfg.NumberOfAccounts)
	return teardown.Teardown(ctx, logger, cfg, loaded.Live, stateFile)
}
