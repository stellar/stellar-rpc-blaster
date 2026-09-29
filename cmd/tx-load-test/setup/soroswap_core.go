package setup

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const soroswapStandaloneNetworkPassphrase = "Standalone Network ; February 2017"

type soroswapCoreStep struct{}

func (soroswapCoreStep) Name() string { return "deploy Soroswap core" }

func (soroswapCoreStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	factoryContract, routerContract := resolvedSoroswapContracts(cfg, st)
	if (factoryContract == "") != (routerContract == "") {
		return fmt.Errorf("soroswap factory/router configuration is incomplete")
	}
	if factoryContract != "" && routerContract != "" {
		st.SoroswapFactoryContract = factoryContract
		st.SoroswapRouterContract = routerContract
		logger.Infof("using existing Soroswap core: factory=%s router=%s", factoryContract, routerContract)
		return nil
	}
	if !SupportsSoroswapAutoBootstrap(cfg.NetworkPassphrase) {
		return fmt.Errorf("soroswap core contract IDs must be supplied on this network")
	}

	factoryContract, routerContract, err := ensureSoroswapCore(ctx, logger, cfg, st)
	if err != nil {
		return err
	}
	st.SoroswapFactoryContract = factoryContract
	st.SoroswapRouterContract = routerContract
	return nil
}

func SupportsSoroswapAutoBootstrap(networkPassphrase string) bool {
	switch networkPassphrase {
	case soroswapStandaloneNetworkPassphrase, network.FutureNetworkPassphrase:
		return true
	default:
		return false
	}
}

func ensureSoroswapCore(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
) (string, string, error) {
	artifacts, err := loadSoroswapCoreArtifacts(cfg.NetworkPassphrase, st.FeePayerKP.Address())
	if err != nil {
		return "", "", err
	}

	if err := ensureContractWasmUploaded(ctx, logger, st, cfg.NetworkPassphrase, artifacts.pair); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap pair Wasm upload: %w", err)
	}
	if err := ensureContractWasmUploaded(ctx, logger, st, cfg.NetworkPassphrase, artifacts.factory); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap factory Wasm upload: %w", err)
	}
	if err := ensureContractWasmUploaded(ctx, logger, st, cfg.NetworkPassphrase, artifacts.router); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap router Wasm upload: %w", err)
	}

	if err := ensureContractDeployed(ctx, logger, st, cfg.NetworkPassphrase, artifacts.factory); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap factory deployment: %w", err)
	}
	if err := ensureSoroswapFactoryInitialized(ctx, logger, st, cfg.NetworkPassphrase, artifacts.factory.contractIDStr, artifacts.pair.wasmHash); err != nil {
		return "", "", fmt.Errorf("initialize Soroswap factory: %w", err)
	}

	if err := ensureContractDeployed(ctx, logger, st, cfg.NetworkPassphrase, artifacts.router); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap router deployment: %w", err)
	}
	if err := ensureSoroswapRouterInitialized(ctx, logger, st, cfg.NetworkPassphrase, artifacts.router.contractIDStr, artifacts.factory.contractIDStr); err != nil {
		return "", "", fmt.Errorf("initialize Soroswap router: %w", err)
	}

	logger.Infof("Soroswap core ready: factory=%s router=%s", artifacts.factory.contractIDStr, artifacts.router.contractIDStr)
	return artifacts.factory.contractIDStr, artifacts.router.contractIDStr, nil
}
