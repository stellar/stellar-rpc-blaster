package setup

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	ozTokenName   = "BenchToken"
	ozTokenSymbol = "BLT"

	// ozTokenWasmPath is the path to the compiled OZ-style token Wasm file.
	// The contract should be built with supply tracking disabled so that
	// balance updates to different accounts are independent and the ledger
	// can apply them in parallel across multiple apply threads.
	ozTokenWasmPath = "contracts/oz_token.wasm"
)

type ozTokenStep struct{}

func (ozTokenStep) Name() string { return "deploy OZ custom token" }

// Run uploads and initializes a single OpenZeppelin-style custom
// Soroban token contract.
//
// The token is intentionally built WITHOUT supply tracking (i.e. no shared
// TotalSupply storage entry) so that transfers between independent account
// pairs touch disjoint ledger entries and can be parallelized across apply
// threads.
//
// Modules needed from the OZ library set:
//   - fungible     - core ERC-20-like transfer logic
//   - mintable     - allows the fee-payer / admin to mint tokens
//   - (no burnable, no pausable, no access-control beyond basic admin)
//
// Deployment steps:
//  1. Upload (installCode) the Wasm blob via the RPC.
//  2. Create (deployContract) an instance using the returned Wasm hash.
//  3. Call initialize(admin, name, symbol, decimals) on the new instance.
//  4. Mint initial balances for every participant account.
func (ozTokenStep) Run(_ context.Context, logger *log.Entry, _ config.Config, st *state.State) error {
	logger.Infof("uploading OZ token Wasm from %s", ozTokenWasmPath)

	// TODO: read Wasm bytes from ozTokenWasmPath.
	// TODO: submit installCode transaction and record the Wasm hash.

	wasmHash := "" // placeholder
	logger.Infof("OZ token Wasm hash: %s", wasmHash)

	// TODO: submit deployContract transaction using the Wasm hash.
	// contractID := "" // placeholder
	logger.Infof("OZ token contract setup: not yet implemented")

	// TODO: invoke initialize(fee_payer_address, ozTokenName, ozTokenSymbol, 7u32)

	// TODO: mint working balances to every account in state.AccountKPs.

	_ = st

	return fmt.Errorf("OZ token setup: not yet implemented") //nolint:staticcheck // placeholder
}
