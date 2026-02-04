package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stellar/go-stellar-sdk/support/strutils"

	blaster "github.com/stellar/stellar-rpc-blaster/internal"
)

var blasterCmdRunner = func(blasterSettings blaster.RuntimeSettings) error {
	blasterInstance := blaster.NewApp()
	return blasterInstance.RunApp(blasterSettings)
}

func Execute() error {
	rootCmd := makeCommands()
	return rootCmd.Execute()
}

// Sets up the (inert) root command and subcommands 'run' and 'generate' with their respective flags
func makeCommands() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "stellar-blaster",
		Short:         "CLI load testing tool for Stellar RPC",
		SilenceErrors: true,
		SilenceUsage:  false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Usage()
		},
	}

	// Run command
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run a load test",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := bindRunCliParameters(cmd.Flags().Lookup("config-path"),
				cmd.Flags().Lookup("network-passphrase"),
				cmd.Flags().Lookup("rpc-url"),
				cmd.Flags().Lookup("duration"),
				cmd.Flags().Lookup("ramp-up"),
				cmd.Flags().Lookup("test-output-path"),
			)
			settings.Mode = blaster.Run
			settings.Ctx = cmd.Context()
			if settings.Ctx == nil {
				settings.Ctx = context.Background()
			}
			return blasterCmdRunner(settings)
		},
	}

	// Generate command (unimplemented :p)
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate load test data",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := bindGenerateCliParameters(cmd.Flags().Lookup("rpc-url"),
				cmd.Flags().Lookup("network-passphrase"),
				cmd.Flags().Lookup("seed-path"),
				cmd.Flags().Lookup("ledger-window"),
			)
			settings.Mode = blaster.Generate
			settings.Ctx = cmd.Context()
			if settings.Ctx == nil {
				settings.Ctx = context.Background()
			}
			return blasterCmdRunner(settings)
		},
	}

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(generateCmd)

	commonFlags := pflag.NewFlagSet("common_flags", pflag.ExitOnError)
	commonFlags.String("network-passphrase", "", "Network passphrase for the target network")
	commonFlags.String("rpc-url", "", "Target RPC server URL")

	runCmd.Flags().String("config-path", "", "Path to config TOML file")
	runCmd.Flags().String("test-output-path", "./output/load-test-results.json", "Path to export metrics output file")
	runCmd.Flags().Duration("duration", time.Duration(0), "Duration to run the test (e.g., 5m)")
	runCmd.Flags().Duration("ramp-up", time.Duration(0), "Ramp-up time before reaching target RPS (e.g., 30s)")

	generateCmd.Flags().String("seed-path", "", "Path to seed data file output by generate")
	generateCmd.Flags().Uint32("ledger-window", 1000, "Ledger window size for data generation")

	runCmd.Flags().AddFlagSet(commonFlags)
	viper.BindPFlags(runCmd.Flags())

	return rootCmd
}

// Binds CLI parameters for the 'run' command into RuntimeSettings
// checks both flags and environment variables
func bindRunCliParameters(
	cfgPath *pflag.Flag,
	networkPassphrase *pflag.Flag,
	rpcUrl *pflag.Flag,
	duration *pflag.Flag,
	rampUp *pflag.Flag,
	testOutputPath *pflag.Flag,
) blaster.RuntimeSettings {
	bindFlag := func(flag *pflag.Flag) {
		viper.BindPFlag(flag.Name, flag)
		viper.BindEnv(flag.Name, strutils.KebabToConstantCase(flag.Name))
	}
	bindFlag(cfgPath)
	bindFlag(networkPassphrase)
	bindFlag(rpcUrl)
	bindFlag(duration)
	bindFlag(rampUp)
	bindFlag(testOutputPath)

	settings := blaster.RuntimeSettings{}
	settings.ConfigPath = viper.GetString(cfgPath.Name)
	settings.NetworkPassphrase = viper.GetString(networkPassphrase.Name)
	settings.RpcUrl = viper.GetString(rpcUrl.Name)
	settings.Duration = viper.GetViper().GetDuration(duration.Name)
	settings.RampUp = viper.GetViper().GetDuration(rampUp.Name)
	settings.TestOutputPath = viper.GetString(testOutputPath.Name)

	return settings
}

// Binds CLI parameters for the 'generate' command into RuntimeSettings
// checks both flags and environment variables
func bindGenerateCliParameters(
	rpcUrl *pflag.Flag,
	networkPassphrase *pflag.Flag,
	seedPath *pflag.Flag,
	ledgerWindow *pflag.Flag,
) blaster.RuntimeSettings {
	settings := blaster.RuntimeSettings{}

	viper.BindPFlag(rpcUrl.Name, rpcUrl)
	viper.BindEnv(rpcUrl.Name, strutils.KebabToConstantCase(rpcUrl.Name))
	settings.RpcUrl = viper.GetString(rpcUrl.Name)

	viper.BindPFlag(networkPassphrase.Name, networkPassphrase)
	viper.BindEnv(networkPassphrase.Name, strutils.KebabToConstantCase(networkPassphrase.Name))
	settings.NetworkPassphrase = viper.GetString(networkPassphrase.Name)

	viper.BindPFlag(seedPath.Name, seedPath)
	viper.BindEnv(seedPath.Name, strutils.KebabToConstantCase(seedPath.Name))
	settings.SeedPath = viper.GetString(seedPath.Name)

	if ledgerWindow != nil {
		viper.BindPFlag(ledgerWindow.Name, ledgerWindow)
		viper.BindEnv(ledgerWindow.Name, strutils.KebabToConstantCase(ledgerWindow.Name))
		settings.LedgerWindow = viper.GetUint32(ledgerWindow.Name)
	}

	return settings
}
