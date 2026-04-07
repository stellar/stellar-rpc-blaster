package benchmark

import (
	"context"
	"fmt"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// soroswapMode is the Soroswap-swap benchmark workload.
//
// Each request is routed to one of the two independent liquidity pools with
// equal 50/50 probability. Because each pool's swap modifies only that pool's
// own contract instance storage entry the two pools are independent and can be
// processed by two separate apply threads simultaneously.
type soroswapMode struct{}

func (soroswapMode) Label() string { return "soroswap" }

func (soroswapMode) VerifyReady(_ context.Context, _ *state.State) error {
	return fmt.Errorf("soroswap benchmark is not implemented yet")
}

func (soroswapMode) NewTargeter(_ context.Context, _ string, _ *state.State, _ []*keypair.Full) (vegeta.Targeter, SequenceResetFunc, error) {
	return nil, nil, fmt.Errorf("soroswap benchmark is not implemented yet")
}
