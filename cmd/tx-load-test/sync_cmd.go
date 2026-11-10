package main

import (
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

	stateFile, loaded, err := loadRuntimeStateFromCommand(cmd, state.RuntimePhaseSync)
	if err != nil {
		return err
	}

	ctx, cancel := signalCommandContext(cmd, logger, false)
	defer cancel()

	logger.Infof("loaded state from %s (%d accounts)", stateFile, len(loaded.Persisted.AccountIndices))
	return syncstate.SyncState(ctx, logger, loaded.Live, stateFile, loaded.RPCURL)
}
