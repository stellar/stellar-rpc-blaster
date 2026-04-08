package setup

import (
	"context"
	"fmt"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const soroswapLiquidityDeadlineWindow = 5 * time.Minute

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
	factoryContract, routerContract := resolvedSoroswapContracts(cfg, st)
	if factoryContract == "" || routerContract == "" {
		return fmt.Errorf("soroswap core contracts are not configured")
	}

	factoryID, err := ledger.DecodeContractID(factoryContract)
	if err != nil {
		return fmt.Errorf("decode soroswap factory contract ID: %w", err)
	}
	routerID, err := ledger.DecodeContractID(routerContract)
	if err != nil {
		return fmt.Errorf("decode soroswap router contract ID: %w", err)
	}

	if ok, err := ledger.ContractInstanceExists(ctx, st.RPCClient, factoryID); err != nil {
		return fmt.Errorf("check soroswap factory contract: %w", err)
	} else if !ok {
		return fmt.Errorf("soroswap factory contract %s is missing on-ledger", factoryContract)
	}
	if ok, err := ledger.ContractInstanceExists(ctx, st.RPCClient, routerID); err != nil {
		return fmt.Errorf("check soroswap router contract: %w", err)
	} else if !ok {
		return fmt.Errorf("soroswap router contract %s is missing on-ledger", routerContract)
	}

	reportedFactory, err := soroswapGetFactory(ctx, st, routerContract)
	if err != nil {
		return fmt.Errorf("validate soroswap router -> factory link: %w", err)
	}
	if reportedFactory != factoryContract {
		return fmt.Errorf(
			"soroswap router %s points to factory %s, not %s",
			routerContract, reportedFactory, factoryContract,
		)
	}

	st.SoroswapFactoryContract = factoryContract
	st.SoroswapRouterContract = routerContract
	st.SoroswapPairContracts = st.SoroswapPairContracts[:0]

	for i, pair := range soroswapPairDefs {
		sacA := st.SACs[pair[0]]
		sacB := st.SACs[pair[1]]
		if sacA == "" || sacB == "" {
			return fmt.Errorf("soroswap pool %d requires SAC contract IDs for assets %d/%d", i, pair[0], pair[1])
		}

		pairContract, created, err := ensureSoroswapPair(ctx, logger, cfg.NetworkPassphrase, st, sacA, sacB)
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
	networkPassphrase string,
	st *state.State,
	tokenA string,
	tokenB string,
) (string, bool, error) {
	exists, err := soroswapPairExists(ctx, st, st.SoroswapFactoryContract, tokenA, tokenB)
	if err != nil {
		return "", false, err
	}
	if !exists {
		logger.Infof("creating Soroswap pair for %s / %s", tokenA, tokenB)
		if err := createSoroswapPair(ctx, logger, st, networkPassphrase, st.SoroswapFactoryContract, tokenA, tokenB); err != nil {
			return "", false, fmt.Errorf("create pair: %w", err)
		}
	}

	pairContract, err := soroswapGetPair(ctx, st, st.SoroswapFactoryContract, tokenA, tokenB)
	if err != nil {
		return "", false, fmt.Errorf("fetch pair address: %w", err)
	}
	pairID, err := ledger.DecodeContractID(pairContract)
	if err != nil {
		return "", false, fmt.Errorf("decode pair contract ID: %w", err)
	}
	if ok, err := ledger.ContractInstanceExists(ctx, st.RPCClient, pairID); err != nil {
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
	factoryID, err := ledger.DecodeContractID(factoryContract)
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
	contractID, err := ledger.DecodeContractID(contractIDStr)
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
	encoded, err := ledger.EncodeContractID(contractID)
	if err != nil {
		return "", fmt.Errorf("encode contract address: %w", err)
	}
	return encoded, nil
}

func resolvedSoroswapContracts(cfg config.Config, st *state.State) (string, string) {
	factoryContract := cfg.SoroswapFactoryContract
	if factoryContract == "" {
		factoryContract = st.SoroswapFactoryContract
	}
	routerContract := cfg.SoroswapRouterContract
	if routerContract == "" {
		routerContract = st.SoroswapRouterContract
	}
	return factoryContract, routerContract
}

func scValAccountAddress(value xdr.ScVal) (string, error) {
	address, ok := value.GetAddress()
	if !ok {
		return "", fmt.Errorf("expected address return value, got %s", value.Type.String())
	}
	accountID, ok := address.GetAccountId()
	if !ok {
		return "", fmt.Errorf("expected account address return value, got %s", address.Type.String())
	}
	encoded, err := accountID.GetAddress()
	if err != nil {
		return "", fmt.Errorf("encode account address: %w", err)
	}
	return encoded, nil
}

type liquidityStep struct{}

func (liquidityStep) Name() string { return "inject Soroswap liquidity" }

// Run seeds each benchmark Soroswap pair with the configured amount of both
// SAC assets. The fee payer is the initial LP. Re-running setup is idempotent:
// empty pools are seeded, while already-seeded pools are left untouched.
func (liquidityStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	if cfg.LiquidityPerPool <= 0 {
		return fmt.Errorf("liquidity-per-pool must be > 0 for soroswap setup")
	}
	_, routerContract := resolvedSoroswapContracts(cfg, st)
	if routerContract == "" {
		return fmt.Errorf("soroswap router contract is not configured")
	}
	if len(st.SoroswapPairContracts) != len(soroswapPairDefs) {
		return fmt.Errorf("expected %d soroswap pair contracts, found %d", len(soroswapPairDefs), len(st.SoroswapPairContracts))
	}

	for i, pair := range soroswapPairDefs {
		tokenA := st.SACs[pair[0]]
		tokenB := st.SACs[pair[1]]
		pairContract := st.SoroswapPairContracts[i]
		if tokenA == "" || tokenB == "" || pairContract == "" {
			return fmt.Errorf("soroswap pool %d is missing token or pair contract state", i)
		}

		reserveA, err := soroswapTokenBalance(ctx, st, tokenA, pairContract)
		if err != nil {
			return fmt.Errorf("pool %d reserve A: %w", i, err)
		}
		reserveB, err := soroswapTokenBalance(ctx, st, tokenB, pairContract)
		if err != nil {
			return fmt.Errorf("pool %d reserve B: %w", i, err)
		}

		switch {
		case i128IsZero(reserveA) && i128IsZero(reserveB):
			logger.Infof("soroswap pool %d: seeding %d units per token into %s", i, cfg.LiquidityPerPool, pairContract)
			if err := addSoroswapLiquidity(ctx, logger, st, cfg.NetworkPassphrase, routerContract, tokenA, tokenB, cfg.LiquidityPerPool); err != nil {
				return fmt.Errorf("pool %d seed liquidity: %w", i, err)
			}
		case !i128IsZero(reserveA) && !i128IsZero(reserveB):
			logger.Infof("soroswap pool %d: already seeded, skipping (reserveA=%d reserveB=%d)", i, reserveA.Lo, reserveB.Lo)
		default:
			return fmt.Errorf("pool %d has inconsistent reserves (reserveA=%d reserveB=%d)", i, reserveA.Lo, reserveB.Lo)
		}
	}

	return nil
}

func addSoroswapLiquidity(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	routerContract string,
	tokenA string,
	tokenB string,
	amount int64,
) error {
	routerID, err := ledger.DecodeContractID(routerContract)
	if err != nil {
		return fmt.Errorf("decode router contract ID: %w", err)
	}
	provider, err := addressScVal(st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("encode LP address: %w", err)
	}
	tokenArgs, err := contractAddressArgs(tokenA, tokenB)
	if err != nil {
		return fmt.Errorf("encode token addresses: %w", err)
	}
	deadline := time.Now().Add(soroswapLiquidityDeadlineWindow).Unix()
	if deadline <= 0 {
		return fmt.Errorf("invalid liquidity deadline")
	}

	args := append(tokenArgs,
		i128ScVal(amount),
		i128ScVal(amount),
		i128ScVal(amount),
		i128ScVal(amount),
		provider,
		u64ScVal(uint64(deadline)),
	)
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &routerID,
		},
		FunctionName: "add_liquidity",
		Args:         args,
	}
	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &invokeArgs,
		},
		Auth:          sourceAccountContractAuth(invokeArgs),
		SourceAccount: st.FeePayerKP.Address(),
	}
	return state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op)
}

func soroswapTokenBalance(ctx context.Context, st *state.State, tokenContract, ownerAddress string) (xdr.Int128Parts, error) {
	ownerVal, err := addressScVal(ownerAddress)
	if err != nil {
		return xdr.Int128Parts{}, fmt.Errorf("encode owner address %s: %w", ownerAddress, err)
	}
	result, err := simulateReadonlyContractCall(ctx, st, tokenContract, "balance", xdr.ScVec{ownerVal})
	if err != nil {
		return xdr.Int128Parts{}, err
	}
	balance, ok := result.GetI128()
	if !ok {
		return xdr.Int128Parts{}, fmt.Errorf("balance returned %s, want i128", result.Type.String())
	}
	return balance, nil
}

func i128IsZero(value xdr.Int128Parts) bool {
	return value.Hi == 0 && value.Lo == 0
}

func u64ScVal(value uint64) xdr.ScVal {
	v := xdr.Uint64(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}
}
