package cli

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stellar/go-stellar-sdk/support/strutils"

	blaster "github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config"
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
				cmd.Flags().Lookup("step-interval"),
				cmd.Flags().Lookup("test-output-path"),
				cmd.Flags().Lookup("input-data-path"),
				cmd.Flags().Lookup("serial"),
				cmd.Flags().Lookup("error-percent"),
			)
			settings.Mode = config.Run
			settings.Ctx = cmd.Context()
			if settings.Ctx == nil {
				settings.Ctx = context.Background()
			}
			return blasterCmdRunner(settings)
		},
	}

	// Generate command
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate load test data",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := bindGenerateCliParameters(cmd.Flags().Lookup("rpc-url"),
				cmd.Flags().Lookup("output"),
				cmd.Flags().Lookup("ledger-window"),
				cmd.Flags().Lookup("count"),
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
	runCmd.Flags().String("test-output-path", "./output", "Base directory for run output (logs and results)")
	runCmd.Flags().String("input-data-path", "", "Path to seed data file output by generate, required for data-dependent endpoints")
	runCmd.Flags().Duration("duration", time.Duration(0), "Duration to run the test (e.g., 5m)")
	runCmd.Flags().Duration("ramp-up", time.Duration(0), "Ramp-up time before reaching target RPS (e.g., 30s)")
	runCmd.Flags().Duration("step-interval", time.Second*5, "Interval between steps during the test (e.g., 5s)")
	runCmd.Flags().Bool("serial", false, "Run endpoints one at a time sequentially instead of concurrently")
	runCmd.Flags().Int("error-percent", 50, "Threshold for error percentage to kill the test (e.g. 50 means kill at 50%)")

	generateCmd.Flags().String("output", "./output/seed.json", "Path to seed data file output by generate")
	generateCmd.Flags().String("ledger-window", "", "Ledger range as START[,END] for data generation")
	generateCmd.Flags().Uint32("count", 5000, "Number of ledgers to sample from the ledger window")

	runCmd.Flags().AddFlagSet(commonFlags)
	generateCmd.Flags().AddFlagSet(commonFlags)
	viper.BindPFlags(runCmd.Flags())
	viper.BindPFlags(generateCmd.Flags())

	return rootCmd
}

// Binds CLI parameters for the 'run' command into RuntimeSettings
// checks both flags and environment variables
func bindRunCliParameters(
	cfgPath *pflag.Flag,
	rpcUrl *pflag.Flag,
	duration *pflag.Flag,
	rampUp *pflag.Flag,
	stepInterval *pflag.Flag,
	testOutputPath *pflag.Flag,
	inputDataPath *pflag.Flag,
	serial *pflag.Flag,
	errorPercent *pflag.Flag,
) config.RuntimeSettings {
	bindFlag := func(flag *pflag.Flag) {
		viper.BindPFlag(flag.Name, flag)
		viper.BindEnv(flag.Name, strutils.KebabToConstantCase(flag.Name))
	}
	bindFlag(cfgPath)
	bindFlag(rpcUrl)
	bindFlag(duration)
	bindFlag(rampUp)
	bindFlag(stepInterval)
	bindFlag(testOutputPath)
	bindFlag(inputDataPath)
	bindFlag(serial)
	bindFlag(errorPercent)
	settings := config.RuntimeSettings{}
	settings.ConfigPath = viper.GetString(cfgPath.Name)
	settings.RpcUrl = viper.GetString(rpcUrl.Name)
	settings.Duration = viper.GetViper().GetDuration(duration.Name)
	settings.RampUp = viper.GetViper().GetDuration(rampUp.Name)
	settings.StepInterval = viper.GetViper().GetDuration(stepInterval.Name)
	settings.TestOutputPath = viper.GetString(testOutputPath.Name)
	settings.InputDataPath = viper.GetString(inputDataPath.Name)
	settings.Serial = viper.GetBool(serial.Name)
	settings.ErrorPercent = viper.GetInt(errorPercent.Name)

	return settings
}

// Binds CLI parameters for the 'generate' command into RuntimeSettings
// checks both flags and environment variables
func bindGenerateCliParameters(
	rpcUrl *pflag.Flag,
	outputPath *pflag.Flag,
	ledgerWindow *pflag.Flag,
	count *pflag.Flag,
) config.RuntimeSettings {
	bindFlag := func(flag *pflag.Flag) {
		viper.BindPFlag(flag.Name, flag)
		viper.BindEnv(flag.Name, strutils.KebabToConstantCase(flag.Name))
	}
	bindFlag(rpcUrl)
	bindFlag(outputPath)
	bindFlag(ledgerWindow)
	bindFlag(count)
	settings := config.RuntimeSettings{}
	settings.RpcUrl = viper.GetString(rpcUrl.Name)
	settings.OutputPath = viper.GetString(outputPath.Name)
	settings.LedgerWindow = parseLedgerWindow(viper.GetString(ledgerWindow.Name))
	settings.Count = viper.GetUint32(count.Name)

	return settings
}

// parseLedgerWindow parses a comma-separated string of up to 2 ledger sequence numbers.
// Returns nil for an empty string (no window specified).
func parseLedgerWindow(s string) []uint32 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]uint32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			continue
		}
		result = append(result, uint32(v))
	}
	return result
}
