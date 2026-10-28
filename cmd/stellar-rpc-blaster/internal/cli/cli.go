package cli

import (
	"context"
	"fmt"
	"math"
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
			settings, err := bindRunCliParameters(cmd.Flags().Lookup("config-path"),
				cmd.Flags().Lookup("rpc-url"),
				cmd.Flags().Lookup("duration"),
				cmd.Flags().Lookup("ramp-up"),
				cmd.Flags().Lookup("step-interval"),
				cmd.Flags().Lookup("test-output-path"),
				cmd.Flags().Lookup("input-data-path"),
				cmd.Flags().Lookup("serial"),
				cmd.Flags().Lookup("cooloff"),
				cmd.Flags().Lookup("error-percent"),
				cmd.Flags().Lookup("query-window"),
			)
			if err != nil {
				return err
			}
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
			settings, err := bindGenerateCliParameters(cmd.Flags().Lookup("rpc-url"),
				cmd.Flags().Lookup("output"),
				cmd.Flags().Lookup("ledger-window"),
				cmd.Flags().Lookup("count"),
			)
			if err != nil {
				return err
			}
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
	runCmd.Flags().String("test-output-path", "./output", "Results output: a directory (timestamped test-results-*.json created inside) or an explicit .json file path")
	runCmd.Flags().String("input-data-path", "", "Path to seed data file output by generate, required for data-dependent endpoints")
	runCmd.Flags().Duration("duration", time.Duration(0), "Duration to run the test (e.g., 5m)")
	runCmd.Flags().Duration("ramp-up", time.Duration(0), "Ramp-up time before reaching target RPS (e.g., 30s)")
	runCmd.Flags().Duration("step-interval", time.Second*5, "Interval between steps during the test (e.g., 5s)")
	runCmd.Flags().Bool("serial", false, "Run endpoints one at a time sequentially instead of concurrently")
	runCmd.Flags().Duration("cooloff", time.Duration(0), "Delay between endpoints in serial mode so one endpoint's failures don't cascade into the next (e.g. 30s)")
	runCmd.Flags().Int("error-percent", 50, "Threshold for error percentage to kill the test (e.g. 50 means kill at 50%)")
	runCmd.Flags().String("query-window", "", "Ledgers to place requests in, as START, START,END or -N for the newest N; clamped to the target's retention minus a margin for ledgers aging out mid-run (default: all of it)")

	generateCmd.Flags().String("output", "./output/seed.json", "Path to seed data file output by generate")
	generateCmd.Flags().String("ledger-window", "", "Ledgers to sample seed data from, as START, START,END or -N for the newest N")
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
	cooloff *pflag.Flag,
	errorPercent *pflag.Flag,
	queryWindow *pflag.Flag,
) (config.RuntimeSettings, error) {
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
	bindFlag(cooloff)
	bindFlag(errorPercent)
	bindFlag(queryWindow)
	settings := config.RuntimeSettings{}
	settings.ConfigPath = viper.GetString(cfgPath.Name)
	settings.RpcUrl = viper.GetString(rpcUrl.Name)
	settings.Duration = viper.GetViper().GetDuration(duration.Name)
	settings.RampUp = viper.GetViper().GetDuration(rampUp.Name)
	settings.StepInterval = viper.GetViper().GetDuration(stepInterval.Name)
	settings.TestOutputPath = viper.GetString(testOutputPath.Name)
	settings.InputDataPath = viper.GetString(inputDataPath.Name)
	settings.Serial = viper.GetBool(serial.Name)
	settings.Cooloff = viper.GetViper().GetDuration(cooloff.Name)
	settings.ErrorPercent = viper.GetInt(errorPercent.Name)
	var err error
	settings.QueryWindow, err = parseLedgerWindow(queryWindow.Name, viper.GetString(queryWindow.Name))
	return settings, err
}

// Binds CLI parameters for the 'generate' command into RuntimeSettings
// checks both flags and environment variables
func bindGenerateCliParameters(
	rpcUrl *pflag.Flag,
	outputPath *pflag.Flag,
	ledgerWindow *pflag.Flag,
	count *pflag.Flag,
) (config.RuntimeSettings, error) {
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
	settings.Count = viper.GetUint32(count.Name)
	var err error
	settings.LedgerWindow, err = parseLedgerWindow(ledgerWindow.Name, viper.GetString(ledgerWindow.Name))
	return settings, err
}

// parseLedgerWindow parses the named flag's ledger window: START or START,END as
// absolute ledgers, or -N for the newest N; nil for an empty value.
func parseLedgerWindow(flag, s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > 2 {
		return nil, fmt.Errorf("%s must have at most 2 values, got %d", flag, len(parts))
	}
	window := make([]int64, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil || v > math.MaxUint32 || v < 0 && i > 0 || v == 0 && strings.HasPrefix(p, "-") {
			return nil, fmt.Errorf("%s: %q is not a ledger sequence", flag, p)
		}
		window[i] = v
	}
	if len(window) == 2 && window[0] < 0 {
		return nil, fmt.Errorf("%s: -N (the newest N ledgers) takes no END", flag)
	}
	return window, nil
}
