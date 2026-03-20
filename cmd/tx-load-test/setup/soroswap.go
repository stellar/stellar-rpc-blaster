package setup

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// soroswapPairDefs enumerates the SAC index pairs that make up each pool.
// Pool 0: BLTA / BLTB   (SAC indices 0 and 1)
// Pool 1: BLTB / BLTC   (SAC indices 1 and 2)
//
// Using 2 independent pools means the Soroswap benchmark can run with 2
// parallel apply threads, one per pool, without write-set conflicts between
// the two pools' instance storage entries.
var soroswapPairDefs = [2][2]int{
	{0, 1},
	{1, 2},
}

// soroswapFactoryContractID is the well-known Soroswap factory contract
// address on the target network.  Override via --soroswap-factory flag.
//
// TODO: make this a config field rather than a compile-time constant.
const soroswapFactoryContractID = "" // populate before use

type soroswapPoolsStep struct{}

func (soroswapPoolsStep) Name() string { return "deploy Soroswap pools" }

// Run deploys 2 Soroswap liquidity pool pair contracts via the
// Soroswap factory and stores their contract IDs in State.
//
// A Soroswap swap modifies the pair contract's instance storage entry, so two
// concurrent swap transactions targeting the same pool conflict.  By using 2
// independent pools the benchmark can drive both apply threads at full speed.
func (soroswapPoolsStep) Run(_ context.Context, logger *log.Entry, _ config.Config, st *state.State) error {
	for i, pair := range soroswapPairDefs {
		sacA := st.SACs[pair[0]]
		sacB := st.SACs[pair[1]]

		logger.Infof("creating Soroswap pool %d: %s / %s", i, sacA, sacB)
		// TODO: invoke factory.create_pair(sacA, sacB) and extract pair contract
		//       ID from the InvokeHostFunction result or TransactionMeta.
		_ = sacA
		_ = sacB
	}

	return nil
}

type liquidityStep struct{}

func (liquidityStep) Name() string { return "inject Soroswap liquidity" }

// Run deposits cfg.LiquidityPerPool units of each token into both
// Soroswap pools.  The fee payer acts as the liquidity provider.
//
// Exact deposit amounts and slippage bounds are TBD - the assumption is that
// no additional per-account state needs to be set up beyond what already
// exists in the SAC trustlines.
func (liquidityStep) Run(_ context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	_ = st
	logger.Infof("liquidity injection: not yet implemented (cfg.LiquidityPerPool=%d)", cfg.LiquidityPerPool)

	// TODO: iterate over deployed pool contracts and inject liquidity.
	// TODO: approve cfg.LiquidityPerPool for each pool contract on both SAC
	//       contracts (SAC.approve is required before the pool can pull tokens).
	// TODO: invoke pool.add_liquidity(amount_a_desired, amount_b_desired,
	//       amount_a_min, amount_b_min, to, deadline) on each pool contract.

	return fmt.Errorf("liquidity setup: not yet implemented") //nolint:staticcheck // placeholder
}
