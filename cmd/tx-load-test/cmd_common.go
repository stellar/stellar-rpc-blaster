package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/setup"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const standaloneNetworkPassphrase = "Standalone Network ; February 2017"

var knownNetworks = map[string]string{
	"testnet":    network.TestNetworkPassphrase,
	"futurenet":  network.FutureNetworkPassphrase,
	"mainnet":    network.PublicNetworkPassphrase,
	"standalone": standaloneNetworkPassphrase,
}

func forceExitOnSecondSignal(logger *log.Entry) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Warnf("received second signal (%s)  -- force exiting", sig)
		os.Exit(1)
	}()
}

func addCommonFlags(cmd *cobra.Command) {
	cmd.Flags().String("rpc-url", "", "Stellar RPC HTTP endpoint (required)")
	cmd.Flags().String("network", "", `Network shorthand: testnet | futurenet | mainnet | standalone (required)`)
	cmd.Flags().String("log-level", "info", "Log verbosity: debug | info | warn | error")
	cmd.Flags().String("state-file", state.DefaultStateFile, "Path to the state JSON file")
	_ = cmd.MarkFlagRequired("rpc-url")
	_ = cmd.MarkFlagRequired("network")
}

func makeLogger(cmd *cobra.Command, service string) (*log.Entry, error) {
	levelStr, err := cmd.Flags().GetString("log-level")
	if err != nil {
		return nil, err
	}
	level, err := logrus.ParseLevel(levelStr)
	if err != nil {
		return nil, fmt.Errorf("invalid --log-level %q: must be debug, info, warn, or error", levelStr)
	}
	logger := log.New().WithField("service", service)
	logger.SetLevel(level)
	return logger, nil
}

func addRuntimeStatePreflightFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("skip-account-preflight", false, "Skip the early on-chain existence check for a small sample of participant accounts")
	cmd.Flags().Int("account-preflight-sample", state.DefaultRuntimeAccountPreflightSampleSize, "How many participant accounts to check on-chain during runtime state preflight")
}

func commonConfig(cmd *cobra.Command, cfg *config.Config) error {
	var err error
	if cfg.RPCURL, err = cmd.Flags().GetString("rpc-url"); err != nil {
		return err
	}
	cfg.FeePayerSeed = os.Getenv("TX_LOAD_TEST_FEE_PAYER_SEED")

	networkName, err := cmd.Flags().GetString("network")
	if err != nil {
		return err
	}
	passphrase, ok := knownNetworks[networkName]
	if !ok {
		return fmt.Errorf("unknown network %q: must be one of testnet, futurenet, mainnet, standalone", networkName)
	}
	cfg.NetworkPassphrase = passphrase
	return nil
}

func validateSoroswapSetupConfig(cfg config.Config) error {
	factoryProvided := cfg.SoroswapFactoryContract != ""
	routerProvided := cfg.SoroswapRouterContract != ""
	if factoryProvided != routerProvided {
		return fmt.Errorf("--soroswap-factory and --soroswap-router must either both be set or both be omitted")
	}
	if factoryProvided {
		return nil
	}
	if setup.SupportsSoroswapAutoBootstrap(cfg.NetworkPassphrase) {
		return nil
	}
	return fmt.Errorf("--soroswap-factory and --soroswap-router are required for setup on this network")
}
