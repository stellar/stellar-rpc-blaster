package setup

import (
	"context"
	"fmt"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

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

type soroswapPoolsStep struct{}

func (soroswapPoolsStep) Name() string { return "deploy Soroswap pools" }

// Run validates the supplied Soroswap factory/router contracts and creates or
// reuses the benchmark pair contracts via the factory, storing the resulting
// pair IDs in State.
func (soroswapPoolsStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	if cfg.Mode != config.ModeSoroswap {
		return nil
	}

	factoryID, err := decodeContractID(cfg.SoroswapFactoryContract)
	if err != nil {
		return fmt.Errorf("decode soroswap factory contract ID: %w", err)
	}
	routerID, err := decodeContractID(cfg.SoroswapRouterContract)
	if err != nil {
		return fmt.Errorf("decode soroswap router contract ID: %w", err)
	}

	if ok, err := contractInstanceExists(ctx, st.RPCClient, factoryID); err != nil {
		return fmt.Errorf("check soroswap factory contract: %w", err)
	} else if !ok {
		return fmt.Errorf("soroswap factory contract %s is missing on-ledger", cfg.SoroswapFactoryContract)
	}
	if ok, err := contractInstanceExists(ctx, st.RPCClient, routerID); err != nil {
		return fmt.Errorf("check soroswap router contract: %w", err)
	} else if !ok {
		return fmt.Errorf("soroswap router contract %s is missing on-ledger", cfg.SoroswapRouterContract)
	}

	reportedFactory, err := soroswapGetFactory(ctx, st, cfg.SoroswapRouterContract)
	if err != nil {
		return fmt.Errorf("validate soroswap router -> factory link: %w", err)
	}
	if reportedFactory != cfg.SoroswapFactoryContract {
		return fmt.Errorf(
			"soroswap router %s points to factory %s, not %s",
			cfg.SoroswapRouterContract, reportedFactory, cfg.SoroswapFactoryContract,
		)
	}

	st.SoroswapFactoryContract = cfg.SoroswapFactoryContract
	st.SoroswapRouterContract = cfg.SoroswapRouterContract
	st.SoroswapPairContracts = st.SoroswapPairContracts[:0]

	for i, pair := range soroswapPairDefs {
		sacA := st.SACs[pair[0]]
		sacB := st.SACs[pair[1]]
		if sacA == "" || sacB == "" {
			return fmt.Errorf("soroswap pool %d requires SAC contract IDs for assets %d/%d", i, pair[0], pair[1])
		}

		pairContract, created, err := ensureSoroswapPair(ctx, logger, cfg, st, sacA, sacB)
		if err != nil {
			return fmt.Errorf("soroswap pool %d (%s/%s): %w", i, sacA, sacB, err)
		}
		logger.Infof("soroswap pool %d: pair=%s created=%t", i, pairContract, created)
		st.SoroswapPairContracts = append(st.SoroswapPairContracts, pairContract)
	}

	return nil
}

func ensureSoroswapPair(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	tokenA string,
	tokenB string,
) (string, bool, error) {
	exists, err := soroswapPairExists(ctx, st, cfg.SoroswapFactoryContract, tokenA, tokenB)
	if err != nil {
		return "", false, err
	}
	if !exists {
		logger.Infof("creating Soroswap pair for %s / %s", tokenA, tokenB)
		if err := createSoroswapPair(ctx, logger, st, cfg.NetworkPassphrase, cfg.SoroswapFactoryContract, tokenA, tokenB); err != nil {
			return "", false, fmt.Errorf("create pair: %w", err)
		}
	}

	pairContract, err := soroswapGetPair(ctx, st, cfg.SoroswapFactoryContract, tokenA, tokenB)
	if err != nil {
		return "", false, fmt.Errorf("fetch pair address: %w", err)
	}
	pairID, err := decodeContractID(pairContract)
	if err != nil {
		return "", false, fmt.Errorf("decode pair contract ID: %w", err)
	}
	if ok, err := contractInstanceExists(ctx, st.RPCClient, pairID); err != nil {
		return "", false, fmt.Errorf("check pair contract instance: %w", err)
	} else if !ok {
		return "", false, fmt.Errorf("pair contract %s not found on-ledger after provisioning", pairContract)
	}

	return pairContract, !exists, nil
}

func soroswapGetFactory(ctx context.Context, st *state.State, routerContract string) (string, error) {
	result, err := simulateReadonlyContractCall(ctx, st, routerContract, "get_factory", nil)
	if err != nil {
		return "", err
	}
	return scValContractAddress(result)
}

func soroswapPairExists(ctx context.Context, st *state.State, factoryContract, tokenA, tokenB string) (bool, error) {
	args, err := contractAddressArgs(tokenA, tokenB)
	if err != nil {
		return false, err
	}
	result, err := simulateReadonlyContractCall(ctx, st, factoryContract, "pair_exists", args)
	if err != nil {
		return false, err
	}
	value, ok := result.GetB()
	if !ok {
		return false, fmt.Errorf("pair_exists returned %s, want bool", result.Type.String())
	}
	return value, nil
}

func soroswapGetPair(ctx context.Context, st *state.State, factoryContract, tokenA, tokenB string) (string, error) {
	args, err := contractAddressArgs(tokenA, tokenB)
	if err != nil {
		return "", err
	}
	result, err := simulateReadonlyContractCall(ctx, st, factoryContract, "get_pair", args)
	if err != nil {
		return "", err
	}
	return scValContractAddress(result)
}

func createSoroswapPair(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	factoryContract string,
	tokenA string,
	tokenB string,
) error {
	factoryID, err := decodeContractID(factoryContract)
	if err != nil {
		return err
	}
	args, err := contractAddressArgs(tokenA, tokenB)
	if err != nil {
		return err
	}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &factoryID,
		},
		FunctionName: "create_pair",
		Args:         args,
	}
	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &invokeArgs,
		},
		SourceAccount: st.FeePayerKP.Address(),
	}
	return state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op)
}

func simulateReadonlyContractCall(
	ctx context.Context,
	st *state.State,
	contractIDStr string,
	functionName string,
	args xdr.ScVec,
) (xdr.ScVal, error) {
	contractID, err := decodeContractID(contractIDStr)
	if err != nil {
		return xdr.ScVal{}, err
	}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		},
		FunctionName: xdr.ScSymbol(functionName),
		Args:         args,
	}
	op := txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &invokeArgs,
		},
		SourceAccount: st.FeePayerKP.Address(),
		Ext:           xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}},
	}
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: st.FeePayerKP.Address(), Sequence: 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              state.InclusionFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
	})
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("build read-only contract call: %w", err)
	}
	b64, err := tx.Base64()
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("marshal read-only contract call: %w", err)
	}
	simCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	simResp, err := st.RPCClient.SimulateTransaction(simCtx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("simulate contract call: %w", err)
	}
	if simResp.Error != "" {
		return xdr.ScVal{}, fmt.Errorf("simulate contract call: %s", simResp.Error)
	}
	if len(simResp.Results) != 1 {
		return xdr.ScVal{}, fmt.Errorf("simulate contract call returned %d results, want 1", len(simResp.Results))
	}
	if simResp.Results[0].ReturnValueXDR == nil {
		return xdr.ScVal{}, fmt.Errorf("simulate contract call returned no value")
	}
	var result xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(*simResp.Results[0].ReturnValueXDR, &result); err != nil {
		return xdr.ScVal{}, fmt.Errorf("decode contract return value: %w", err)
	}
	return result, nil
}

func contractAddressArgs(addresses ...string) (xdr.ScVec, error) {
	args := make(xdr.ScVec, 0, len(addresses))
	for _, address := range addresses {
		val, err := addressScVal(address)
		if err != nil {
			return nil, fmt.Errorf("encode contract address %s: %w", address, err)
		}
		args = append(args, val)
	}
	return args, nil
}

func scValContractAddress(value xdr.ScVal) (string, error) {
	address, ok := value.GetAddress()
	if !ok {
		return "", fmt.Errorf("expected address return value, got %s", value.Type.String())
	}
	contractID, ok := address.GetContractId()
	if !ok {
		return "", fmt.Errorf("expected contract address return value, got %s", address.Type.String())
	}
	encoded, err := strkey.Encode(strkey.VersionByteContract, contractID[:])
	if err != nil {
		return "", fmt.Errorf("encode contract address: %w", err)
	}
	return encoded, nil
}

func decodeContractID(contractIDStr string) (xdr.ContractId, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, contractIDStr)
	if err != nil {
		return xdr.ContractId{}, err
	}
	var contractID xdr.ContractId
	copy(contractID[:], raw)
	return contractID, nil
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
