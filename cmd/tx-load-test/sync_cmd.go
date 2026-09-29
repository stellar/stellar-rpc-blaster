package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/syncstate"
)

func buildSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile the state file with on-chain account existence",
		Long: `sync checks which participant accounts in the state file still exist
on-chain and removes any that are missing. Use this when the state file gets
out of sync with the network (e.g. after a manual merge or a network reset).`,
		RunE: runSync,
	}

	addRuntimeStatePreflightFlags(cmd)
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("rpc-url", "", "Override the RPC URL stored in the state JSON file")
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

	loaded, err := state.LoadRuntimeStateWithOptions(cmd.Context(), state.RuntimePhaseSync, stateFile, os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED"), rpcURL, state.RuntimeLoadOptions{
		VerifyAccountsExist: !skipAccountPreflight,
		AccountCheckSample:  accountPreflightSample,
	})
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts)", stateFile, len(loaded.Persisted.AccountIndices))
	return syncstate.SyncState(ctx, logger, loaded.Live, stateFile, loaded.RPCURL)
}
