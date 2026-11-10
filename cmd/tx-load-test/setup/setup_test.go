package setup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
)

func TestSetupStepsAlwaysIncludeSoroswapBootstrapAndLiquidity(t *testing.T) {
	steps := setupSteps(config.Config{Mode: config.ModeSoroswap})
	coreAt := -1
	poolsAt := -1
	liquidityAt := -1
	ozAt := -1
	for i, step := range steps {
		switch step.(type) {
		case soroswapCoreStep:
			coreAt = i
		case soroswapPoolsStep:
			poolsAt = i
		case liquidityStep:
			liquidityAt = i
		case ozTokenStep:
			ozAt = i
		}
	}
	require.NotEqual(t, -1, coreAt)
	require.NotEqual(t, -1, poolsAt)
	require.NotEqual(t, -1, liquidityAt)
	require.NotEqual(t, -1, ozAt)
	require.Equal(t, coreAt+1, poolsAt)
	require.Equal(t, poolsAt+1, liquidityAt)
	require.Greater(t, ozAt, liquidityAt)

	steps = setupSteps(config.Config{Mode: config.ModeSACTransfer})
	coreAt = -1
	poolsAt = -1
	liquidityAt = -1
	for _, step := range steps {
		switch step.(type) {
		case soroswapCoreStep:
			coreAt = 1
		case soroswapPoolsStep:
			poolsAt = 1
		case liquidityStep:
			liquidityAt = 1
		}
	}
	require.Equal(t, 1, coreAt)
	require.Equal(t, 1, poolsAt)
	require.Equal(t, 1, liquidityAt)
}
