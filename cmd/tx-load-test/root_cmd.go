package main

import "github.com/spf13/cobra"

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
	root.AddCommand(buildRunCmd())
	root.AddCommand(buildRestoreCmd())
	root.AddCommand(buildTeardownCmd())
	root.AddCommand(buildSyncCmd())
	return root
}
