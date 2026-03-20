package benchmark

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"

	vegeta "github.com/tsenart/vegeta/v12/lib"

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

func (soroswapMode) NewTargeter(ctx context.Context, rpcURL string, state *state.State) (vegeta.Targeter, SequenceResetFunc, error) {
	if len(state.AccountKPs) == 0 {
		return nil, nil, fmt.Errorf("need at least 1 account for Soroswap benchmark, got 0")
	}
	// TODO: validate Soroswap pool contract IDs once setup populates them.

	numAccounts := len(state.AccountKPs)

	return func(t *vegeta.Target) error {
		// 50/50 pool selection.
		poolIdx := rand.IntN(2)

		// Choose a random swapper account.
		swapperIdx := rand.IntN(numAccounts)
		swapper := state.AccountKPs[swapperIdx]
		_ = poolIdx
		_ = swapper

		// TODO: decide swap direction (A->B or B->A) and amount based on current
		//       pool reserves (or use a fixed small amount to avoid excessive slippage).
		// TODO: fetch / cache sequence number for swapper.
		// TODO: build InvokeContractOp: router.swap_exact_tokens_for_tokens(...)
		//       or pair.swap(amount0Out, amount1Out, to, data).
		// TODO: simulate, obtain resource fee + footprint, sign, XDR-encode.
		// TODO: wrap in soroban_sendTransaction JSON-RPC body.

		body := []byte(`{}`) // placeholder

		t.Method = http.MethodPost
		t.URL = rpcURL
		t.Body = body
		t.Header = http.Header{"Content-Type": {"application/json"}}
		return nil
	}, nil, nil
}
