package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stellar/go-stellar-sdk/support/strutils"

	blaster "github.com/stellar/stellar-rpc-blaster/internal"
	"github.com/stellar/stellar-rpc-blaster/internal/config"
)

var blasterCmdRunner = func(blasterSettings config.RuntimeSettings) error {
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
				cmd.Flags().Lookup("rpc-url"),
				cmd.Flags().Lookup("duration"),
				cmd.Flags().Lookup("ramp-up"),
				cmd.Flags().Lookup("test-output-path"),
			)
			settings.Mode = config.Run
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
				cmd.Flags().Lookup("seed-path"),
				cmd.Flags().Lookup("ledger-window"),
			)
			settings.Mode = config.Generate
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
	rpcUrl *pflag.Flag,
	duration *pflag.Flag,
	rampUp *pflag.Flag,
	testOutputPath *pflag.Flag,
) config.RuntimeSettings {
	bindFlag := func(flag *pflag.Flag) {
		viper.BindPFlag(flag.Name, flag)
		viper.BindEnv(flag.Name, strutils.KebabToConstantCase(flag.Name))
	}
	bindFlag(cfgPath)
	bindFlag(rpcUrl)
	bindFlag(duration)
	bindFlag(rampUp)
	bindFlag(testOutputPath)

	settings := config.RuntimeSettings{}
	settings.ConfigPath = viper.GetString(cfgPath.Name)
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
	seedPath *pflag.Flag,
	ledgerWindow *pflag.Flag,
) config.RuntimeSettings {
	bindFlag := func(flag *pflag.Flag) {
		viper.BindPFlag(flag.Name, flag)
		viper.BindEnv(flag.Name, strutils.KebabToConstantCase(flag.Name))
	}
	bindFlag(rpcUrl)
	bindFlag(seedPath)

	settings := config.RuntimeSettings{}
	settings.RpcUrl = viper.GetString(rpcUrl.Name)
	settings.SeedPath = viper.GetString(seedPath.Name)
	settings.LedgerWindow = viper.GetViper().GetUint32(ledgerWindow.Name)

	return settings
}
