package benchmark

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	sharedsoroswap "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroswap"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type soroswapSwapTemplate struct {
	traderAddress string
	invokeArgs    xdr.InvokeContractArgs
	authEntries   []xdr.SorobanAuthorizationEntry
	resources     xdr.SorobanResources
	resourceFee   xdr.Int64
	footprint     xdr.LedgerFootprint
}

func buildSoroswapSwapTemplates(ctx context.Context, st *state.State, txSourceAccounts []*keypair.Full) ([]soroswapSwapTemplate, error) {
	routerID, err := ledger.DecodeContractID(st.SoroswapRouterContract)
	if err != nil {
		return nil, fmt.Errorf("decode soroswap router contract ID: %w", err)
	}

	deadline := uint64(time.Now().Add(soroswapSwapDeadlineWindow).Unix())
	templates := make([]soroswapSwapTemplate, 0, len(sharedsoroswap.BenchmarkPairs)*2)
	representativeTrader := txSourceAccounts[0]
	for i, pair := range sharedsoroswap.BenchmarkPairs {
		pairContract := st.SoroswapPairContracts[i]
		tokenA := st.SACs[pair[0]]
		tokenB := st.SACs[pair[1]]
		if tokenA == "" || tokenB == "" || pairContract == "" {
			return nil, fmt.Errorf("soroswap pool %d is missing token or pair contract state", i)
		}
		reserveA, err := sharedsoroswap.TokenBalance(ctx, st, tokenA, pairContract)
		if err != nil {
			return nil, fmt.Errorf("pool %d reserve A: %w", i, err)
		}
		reserveB, err := sharedsoroswap.TokenBalance(ctx, st, tokenB, pairContract)
		if err != nil {
			return nil, fmt.Errorf("pool %d reserve B: %w", i, err)
		}

		tmplAB, err := presimulateSoroswapSwap(st, routerID, representativeTrader, tokenA, tokenB, sharedsoroswap.SwapAmount(reserveA), deadline)
		if err != nil {
			return nil, fmt.Errorf("pre-simulate soroswap pool %d %s->%s: %w", i, tokenA, tokenB, err)
		}
		templates = append(templates, tmplAB)

		tmplBA, err := presimulateSoroswapSwap(st, routerID, representativeTrader, tokenB, tokenA, sharedsoroswap.SwapAmount(reserveB), deadline)
		if err != nil {
			return nil, fmt.Errorf("pre-simulate soroswap pool %d %s->%s: %w", i, tokenB, tokenA, err)
		}
		templates = append(templates, tmplBA)
	}

	return templates, nil
}

func presimulateSoroswapSwap(
	st *state.State,
	routerID xdr.ContractId,
	trader *keypair.Full,
	inputToken string,
	outputToken string,
	amountIn int64,
	deadline uint64,
) (soroswapSwapTemplate, error) {
	invokeArgs, err := sharedsoroswap.BuildSwapInvokeArgs(routerID, trader.Address(), inputToken, outputToken, amountIn, deadline)
	if err != nil {
		return soroswapSwapTemplate{}, err
	}
	sim, err := simulatePaddedInvokeContractDetailed(st, trader, trader.Address(), invokeArgs)
	if err != nil {
		return soroswapSwapTemplate{}, err
	}

	return soroswapSwapTemplate{
		traderAddress: trader.Address(),
		invokeArgs:    invokeArgs,
		authEntries:   sim.authEntries,
		resources:     sim.resources,
		resourceFee:   sim.resourceFee,
		footprint:     sim.footprint,
	}, nil
}
