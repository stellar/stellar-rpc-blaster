package setup

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroswap"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const soroswapLiquidityDeadlineWindow = 5 * time.Minute

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

	reportedFactory, err := soroswap.GetFactory(ctx, st, routerContract)
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

	for i, pair := range soroswap.BenchmarkPairs {
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
	exists, err := soroswap.PairExists(ctx, st, st.SoroswapFactoryContract, tokenA, tokenB)
	if err != nil {
		return "", false, err
	}
	if !exists {
		logger.Infof("creating Soroswap pair for %s / %s", tokenA, tokenB)
		if err := createSoroswapPair(ctx, logger, st, networkPassphrase, st.SoroswapFactoryContract, tokenA, tokenB); err != nil {
			return "", false, fmt.Errorf("create pair: %w", err)
		}
	}

	pairContract, err := soroswap.GetPair(ctx, st, st.SoroswapFactoryContract, tokenA, tokenB)
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
	args, err := sharedsoroban.ContractAddressArgs(tokenA, tokenB)
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
	return soroswap.SimulateReadonlyContractCall(ctx, st, contractIDStr, functionName, args)
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
	if len(st.SoroswapPairContracts) != len(soroswap.BenchmarkPairs) {
		return fmt.Errorf("expected %d soroswap pair contracts, found %d", len(soroswap.BenchmarkPairs), len(st.SoroswapPairContracts))
	}

	for i, pair := range soroswap.BenchmarkPairs {
		tokenA := st.SACs[pair[0]]
		tokenB := st.SACs[pair[1]]
		pairContract := st.SoroswapPairContracts[i]
		if tokenA == "" || tokenB == "" || pairContract == "" {
			return fmt.Errorf("soroswap pool %d is missing token or pair contract state", i)
		}

		reserveA, err := soroswap.TokenBalance(ctx, st, tokenA, pairContract)
		if err != nil {
			return fmt.Errorf("pool %d reserve A: %w", i, err)
		}
		reserveB, err := soroswap.TokenBalance(ctx, st, tokenB, pairContract)
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
	provider, err := sharedsoroban.AddressScVal(st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("encode LP address: %w", err)
	}
	tokenArgs, err := sharedsoroban.ContractAddressArgs(tokenA, tokenB)
	if err != nil {
		return fmt.Errorf("encode token addresses: %w", err)
	}
	deadline := time.Now().Add(soroswapLiquidityDeadlineWindow).Unix()
	if deadline <= 0 {
		return fmt.Errorf("invalid liquidity deadline")
	}

	args := append(tokenArgs,
		sharedsoroban.I128ScVal(amount),
		sharedsoroban.I128ScVal(amount),
		sharedsoroban.I128ScVal(amount),
		sharedsoroban.I128ScVal(amount),
		provider,
		sharedsoroban.U64ScVal(uint64(deadline)),
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
		Auth:          sharedsoroban.SourceAccountContractAuth(invokeArgs),
		SourceAccount: st.FeePayerKP.Address(),
	}
	return state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op)
}

func i128IsZero(value xdr.Int128Parts) bool {
	return value.Hi == 0 && value.Lo == 0
}
