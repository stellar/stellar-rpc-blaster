package main

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
)

func TestValidateSoroswapSetupConfig(t *testing.T) {
	t.Run("standalone allows auto bootstrap", func(t *testing.T) {
		err := validateSoroswapSetupConfig(config.Config{
			NetworkPassphrase: standaloneNetworkPassphrase,
		})
		require.NoError(t, err)
	})

	t.Run("futurenet allows auto bootstrap", func(t *testing.T) {
		err := validateSoroswapSetupConfig(config.Config{
			NetworkPassphrase: network.FutureNetworkPassphrase,
		})
		require.NoError(t, err)
	})

	t.Run("public networks still require both contracts", func(t *testing.T) {
		err := validateSoroswapSetupConfig(config.Config{
			NetworkPassphrase: network.TestNetworkPassphrase,
		})
		require.EqualError(t, err, "--soroswap-factory and --soroswap-router are required for setup on this network")
	})

	t.Run("partial contract config is rejected", func(t *testing.T) {
		err := validateSoroswapSetupConfig(config.Config{
			NetworkPassphrase:       standaloneNetworkPassphrase,
			SoroswapFactoryContract: "factory",
		})
		require.EqualError(t, err, "--soroswap-factory and --soroswap-router must either both be set or both be omitted")
	})

	t.Run("explicit contracts remain valid", func(t *testing.T) {
		err := validateSoroswapSetupConfig(config.Config{
			NetworkPassphrase:       network.TestNetworkPassphrase,
			SoroswapFactoryContract: "factory",
			SoroswapRouterContract:  "router",
		})
		require.NoError(t, err)
	})
}
