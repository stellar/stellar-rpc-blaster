package soroswap

import (
	"context"
	"fmt"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	readOnlyCallTimeout = 30 * time.Second
	swapReserveDivisor  = 1_000
)

// BenchmarkPairs enumerates the two benchmark Soroswap pools.
var BenchmarkPairs = [2][2]int{{0, 1}, {1, 2}}

func PathScVal(tokenA, tokenB string) (xdr.ScVal, error) {
	path := xdr.ScVec{}
	for _, token := range []string{tokenA, tokenB} {
		val, err := sharedsoroban.AddressScVal(token)
		if err != nil {
			return xdr.ScVal{}, err
		}
		path = append(path, val)
	}
	pathRef := &path
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &pathRef}, nil
}

func BuildSwapInvokeArgs(
	routerID xdr.ContractId,
	traderAddress string,
	inputToken string,
	outputToken string,
	amountIn int64,
	deadline uint64,
) (xdr.InvokeContractArgs, error) {
	trader, err := sharedsoroban.AddressScVal(traderAddress)
	if err != nil {
		return xdr.InvokeContractArgs{}, fmt.Errorf("encode trader address: %w", err)
	}
	path, err := PathScVal(inputToken, outputToken)
	if err != nil {
		return xdr.InvokeContractArgs{}, fmt.Errorf("encode swap path: %w", err)
	}
	return xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &routerID},
		FunctionName:    "swap_exact_tokens_for_tokens",
		Args: xdr.ScVec{
			sharedsoroban.I128ScVal(amountIn),
			sharedsoroban.I128ScVal(0),
			path,
			trader,
			sharedsoroban.U64ScVal(deadline),
		},
	}, nil
}

func SwapAmount(reserve xdr.Int128Parts) int64 {
	if reserve.Hi > 0 {
		return int64(^uint64(0)>>1) / swapReserveDivisor
	}
	if reserve.Lo == 0 {
		return 1
	}
	amount := int64(reserve.Lo / swapReserveDivisor)
	if amount < 1 {
		return 1
	}
	return amount
}

func SimulateReadonlyContractCall(
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
		ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID},
		FunctionName:    xdr.ScSymbol(functionName),
		Args:            args,
	}
	op := txnbuild.InvokeHostFunction{
		HostFunction:  xdr.HostFunction{Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract, InvokeContract: &invokeArgs},
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
	simCtx, cancel := context.WithTimeout(ctx, readOnlyCallTimeout)
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

func GetFactory(ctx context.Context, st *state.State, routerContract string) (string, error) {
	result, err := SimulateReadonlyContractCall(ctx, st, routerContract, "get_factory", nil)
	if err != nil {
		return "", err
	}
	return sharedsoroban.ScValContractAddress(result)
}

func PairExists(ctx context.Context, st *state.State, factoryContract, tokenA, tokenB string) (bool, error) {
	args, err := sharedsoroban.ContractAddressArgs(tokenA, tokenB)
	if err != nil {
		return false, err
	}
	result, err := SimulateReadonlyContractCall(ctx, st, factoryContract, "pair_exists", args)
	if err != nil {
		return false, err
	}
	value, ok := result.GetB()
	if !ok {
		return false, fmt.Errorf("pair_exists returned %s, want bool", result.Type.String())
	}
	return value, nil
}

func GetPair(ctx context.Context, st *state.State, factoryContract, tokenA, tokenB string) (string, error) {
	args, err := sharedsoroban.ContractAddressArgs(tokenA, tokenB)
	if err != nil {
		return "", err
	}
	result, err := SimulateReadonlyContractCall(ctx, st, factoryContract, "get_pair", args)
	if err != nil {
		return "", err
	}
	return sharedsoroban.ScValContractAddress(result)
}

func TokenBalance(ctx context.Context, st *state.State, tokenContract, ownerAddress string) (xdr.Int128Parts, error) {
	ownerVal, err := sharedsoroban.AddressScVal(ownerAddress)
	if err != nil {
		return xdr.Int128Parts{}, fmt.Errorf("encode owner address %s: %w", ownerAddress, err)
	}
	result, err := SimulateReadonlyContractCall(ctx, st, tokenContract, "balance", xdr.ScVec{ownerVal})
	if err != nil {
		return xdr.Int128Parts{}, err
	}
	balance, ok := result.GetI128()
	if !ok {
		return xdr.Int128Parts{}, fmt.Errorf("balance returned %s, want i128", result.Type.String())
	}
	return balance, nil
}
