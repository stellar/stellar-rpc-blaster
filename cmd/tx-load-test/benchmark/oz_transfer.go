package benchmark

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// ozTransferMode is the OZ-token-transfer benchmark workload.
//
// Every request picks two distinct random participant accounts and submits a
// soroban_sendTransaction calling transfer(from, to, amount) on the single OZ
// token contract. Because the OZ token does not track total supply, each
// transfer only touches the source and destination balance entries and is
// independent of all other concurrent transfers.
type ozTransferMode struct{}

func (ozTransferMode) Label() string { return "oz-transfer" }

func (ozTransferMode) NewTargeter(ctx context.Context, rpcURL string, state *state.State) (vegeta.Targeter, SequenceResetFunc, error) {
	if len(state.AccountKPs) < 2 {
		return nil, nil, fmt.Errorf("need at least 2 accounts for OZ transfer benchmark, got %d", len(state.AccountKPs))
	}
	// TODO: validate OZ token contract ID once the setup step populates it.

	numAccounts := len(state.AccountKPs)

	return func(t *vegeta.Target) error {
		// Choose two distinct participant accounts.
		srcIdx := rand.IntN(numAccounts)
		dstIdx := rand.IntN(numAccounts - 1)
		if dstIdx >= srcIdx {
			dstIdx++
		}

		src := state.AccountKPs[srcIdx]
		dst := state.AccountKPs[dstIdx]
		_ = src
		_ = dst

		// TODO: fetch / cache current sequence number for src.
		// TODO: build InvokeContractOp: contractID.transfer(src, dst, amount).
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
