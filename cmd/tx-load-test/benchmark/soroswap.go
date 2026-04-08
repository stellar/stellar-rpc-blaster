package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	soroswapSwapReserveDivisor  = 1_000
	soroswapSwapDeadlineWindow  = 24 * time.Hour
	soroswapReadOnlyCallTimeout = 30 * time.Second
)

var soroswapBenchmarkPairs = [2][2]int{{0, 1}, {1, 2}}

type soroswapSwapTemplate struct {
	traderAddress string
	invokeArgs    xdr.InvokeContractArgs
	authEntries   []xdr.SorobanAuthorizationEntry
	resources     xdr.SorobanResources
	resourceFee   xdr.Int64
	footprint     xdr.LedgerFootprint
}

// soroswapMode is the Soroswap-swap benchmark workload.
//
// Each request is routed to one of the two independent liquidity pools with
// equal 50/50 probability. Because each pool's swap modifies only that pool's
// own contract instance storage entry the two pools are independent and can be
// processed by two separate apply threads simultaneously.
type soroswapMode struct{}

func (soroswapMode) Label() string { return "soroswap" }

func (soroswapMode) VerifyReady(ctx context.Context, st *state.State) error {
	if st.SoroswapFactoryContract == "" {
		return fmt.Errorf("soroswap factory contract ID is empty -- run setup first")
	}
	if st.SoroswapRouterContract == "" {
		return fmt.Errorf("soroswap router contract ID is empty -- run setup first")
	}
	if len(st.SoroswapPairContracts) != len(soroswapBenchmarkPairs) {
		return fmt.Errorf("need %d Soroswap pair contracts, got %d -- rerun setup", len(soroswapBenchmarkPairs), len(st.SoroswapPairContracts))
	}

	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holderAccounts) == 0 {
		return fmt.Errorf("need trustlined participant accounts for Soroswap benchmark -- rerun setup")
	}
	if err := verifyTrustlineBalancesReady(ctx, st, holderAccounts, "Soroswap"); err != nil {
		return err
	}

	if _, err := requireReadyContract(ctx, st, "soroswap factory", st.SoroswapFactoryContract); err != nil {
		return err
	}
	if _, err := requireReadyContract(ctx, st, "soroswap router", st.SoroswapRouterContract); err != nil {
		return err
	}
	reportedFactory, err := benchmarkSoroswapGetFactory(ctx, st, st.SoroswapRouterContract)
	if err != nil {
		return fmt.Errorf("validate soroswap router -> factory link: %w", err)
	}
	if reportedFactory != st.SoroswapFactoryContract {
		return fmt.Errorf("soroswap router %s points to factory %s, not %s", st.SoroswapRouterContract, reportedFactory, st.SoroswapFactoryContract)
	}

	for i, pair := range soroswapBenchmarkPairs {
		pairContract := st.SoroswapPairContracts[i]
		if _, err := requireReadyContract(ctx, st, fmt.Sprintf("soroswap pair[%d]", i), pairContract); err != nil {
			return err
		}

		reserveA, err := benchmarkSoroswapTokenBalance(ctx, st, st.SACs[pair[0]], pairContract)
		if err != nil {
			return fmt.Errorf("fetch soroswap pair[%d] reserve A: %w", i, err)
		}
		reserveB, err := benchmarkSoroswapTokenBalance(ctx, st, st.SACs[pair[1]], pairContract)
		if err != nil {
			return fmt.Errorf("fetch soroswap pair[%d] reserve B: %w", i, err)
		}
		if !ledger.HasPositiveI128(reserveA) || !ledger.HasPositiveI128(reserveB) {
			return fmt.Errorf("soroswap pair[%d] is not seeded with positive reserves -- rerun setup", i)
		}
	}

	return nil
}

func (soroswapMode) NewTargeter(ctx context.Context, rpcURL string, st *state.State, txSourceAccounts []*keypair.Full) (vegeta.Targeter, SequenceResetFunc, error) {
	if len(txSourceAccounts) == 0 {
		return nil, nil, fmt.Errorf("need at least 1 participant account for Soroswap tx sources")
	}
	if st.SoroswapRouterContract == "" {
		return nil, nil, fmt.Errorf("soroswap router contract ID is empty -- run setup first")
	}
	if len(st.SoroswapPairContracts) != len(soroswapBenchmarkPairs) {
		return nil, nil, fmt.Errorf("need %d Soroswap pair contracts, got %d -- rerun setup", len(soroswapBenchmarkPairs), len(st.SoroswapPairContracts))
	}
	if err := verifyTrustlineBalancesReady(ctx, st, txSourceAccounts, "Soroswap"); err != nil {
		return nil, nil, err
	}

	routerID, err := ledger.DecodeContractID(st.SoroswapRouterContract)
	if err != nil {
		return nil, nil, fmt.Errorf("decode soroswap router contract ID: %w", err)
	}

	seqs, err := newSequenceManager(ctx, st, txSourceAccounts, "Soroswap tx-source")
	if err != nil {
		return nil, nil, err
	}

	deadline := uint64(time.Now().Add(soroswapSwapDeadlineWindow).Unix())
	templates := make([]soroswapSwapTemplate, 0, len(soroswapBenchmarkPairs)*2)
	representativeTrader := txSourceAccounts[0]
	for i, pair := range soroswapBenchmarkPairs {
		pairContract := st.SoroswapPairContracts[i]
		tokenA := st.SACs[pair[0]]
		tokenB := st.SACs[pair[1]]
		if tokenA == "" || tokenB == "" || pairContract == "" {
			return nil, nil, fmt.Errorf("soroswap pool %d is missing token or pair contract state", i)
		}
		reserveA, err := benchmarkSoroswapTokenBalance(ctx, st, tokenA, pairContract)
		if err != nil {
			return nil, nil, fmt.Errorf("pool %d reserve A: %w", i, err)
		}
		reserveB, err := benchmarkSoroswapTokenBalance(ctx, st, tokenB, pairContract)
		if err != nil {
			return nil, nil, fmt.Errorf("pool %d reserve B: %w", i, err)
		}

		tmplAB, err := presimulateSoroswapSwap(st, routerID, representativeTrader, tokenA, tokenB, benchmarkSoroswapSwapAmount(reserveA), deadline)
		if err != nil {
			return nil, nil, fmt.Errorf("pre-simulate soroswap pool %d %s->%s: %w", i, tokenA, tokenB, err)
		}
		templates = append(templates, tmplAB)

		tmplBA, err := presimulateSoroswapSwap(st, routerID, representativeTrader, tokenB, tokenA, benchmarkSoroswapSwapAmount(reserveB), deadline)
		if err != nil {
			return nil, nil, fmt.Errorf("pre-simulate soroswap pool %d %s->%s: %w", i, tokenB, tokenA, err)
		}
		templates = append(templates, tmplBA)
	}

	var slotCounter int64
	return func(t *vegeta.Target) error {
		slot := atomic.AddInt64(&slotCounter, 1) - 1
		srcIdx := int(slot % int64(len(txSourceAccounts)))
		templateIdx := int((slot / int64(len(txSourceAccounts))) % int64(len(templates)))
		txSourceKP := txSourceAccounts[srcIdx]
		seq := seqs.Next(srcIdx)
		tmpl := templates[templateIdx]

		invokeArgs, err := rewriteInvokeContractAccount(tmpl.invokeArgs, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap invoke args: %w", err)
		}
		authEntries, err := rewriteSorobanAuthEntriesAccount(tmpl.authEntries, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap auth: %w", err)
		}
		footprint, err := rewriteFootprintAccount(tmpl.footprint, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap footprint: %w", err)
		}

		op := txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &invokeArgs,
			},
			Auth:          authEntries,
			SourceAccount: txSourceKP.Address(),
			Ext: xdr.TransactionExt{
				V: 1,
				SorobanData: &xdr.SorobanTransactionData{
					Resources: xdr.SorobanResources{
						Footprint:     footprint,
						Instructions:  tmpl.resources.Instructions,
						DiskReadBytes: tmpl.resources.DiskReadBytes,
						WriteBytes:    tmpl.resources.WriteBytes,
					},
					ResourceFee: tmpl.resourceFee,
				},
			},
		}

		tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        &txnbuild.SimpleAccount{AccountID: txSourceKP.Address(), Sequence: seq},
			IncrementSequenceNum: false,
			Operations:           []txnbuild.Operation{&op},
			BaseFee:              benchmarkBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
		})
		if err != nil {
			return fmt.Errorf("build Soroswap transaction: %w", err)
		}
		tx, err = tx.Sign(st.NetworkPassphrase, txSourceKP)
		if err != nil {
			return fmt.Errorf("sign Soroswap transaction: %w", err)
		}
		b64, err := tx.Base64()
		if err != nil {
			return fmt.Errorf("marshal Soroswap transaction: %w", err)
		}

		id := slot + 1
		body, err := json.Marshal(rpcJSONBody{
			JSONRPC: "2.0",
			ID:      id,
			Method:  protocol.SendTransactionMethodName,
			Params:  map[string]string{"transaction": b64},
		})
		if err != nil {
			return fmt.Errorf("marshal JSON-RPC body: %w", err)
		}
		t.Method = http.MethodPost
		t.URL = rpcURL
		t.Body = body
		t.Header = http.Header{"Content-Type": {"application/json"}}
		return nil
	}, seqs.ResetFunc(), nil
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
	invokeArgs, err := buildSoroswapSwapInvokeArgs(routerID, trader.Address(), inputToken, outputToken, amountIn, deadline)
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

func buildSoroswapSwapInvokeArgs(
	routerID xdr.ContractId,
	traderAddress string,
	inputToken string,
	outputToken string,
	amountIn int64,
	deadline uint64,
) (xdr.InvokeContractArgs, error) {
	trader, err := benchmarkAddressScVal(traderAddress)
	if err != nil {
		return xdr.InvokeContractArgs{}, fmt.Errorf("encode trader address: %w", err)
	}
	path, err := soroswapPathScVal(inputToken, outputToken)
	if err != nil {
		return xdr.InvokeContractArgs{}, fmt.Errorf("encode swap path: %w", err)
	}
	return xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &routerID},
		FunctionName:    "swap_exact_tokens_for_tokens",
		Args: xdr.ScVec{
			benchmarkI128ScVal(amountIn),
			benchmarkI128ScVal(0),
			path,
			trader,
			benchmarkU64ScVal(deadline),
		},
	}, nil
}

func benchmarkSoroswapSwapAmount(reserve xdr.Int128Parts) int64 {
	if reserve.Hi > 0 {
		return int64(^uint64(0)>>1) / soroswapSwapReserveDivisor
	}
	if reserve.Lo == 0 {
		return 1
	}
	amount := int64(reserve.Lo / soroswapSwapReserveDivisor)
	if amount < 1 {
		return 1
	}
	return amount
}

func benchmarkSoroswapGetFactory(ctx context.Context, st *state.State, routerContract string) (string, error) {
	result, err := benchmarkSimulateReadonlyContractCall(ctx, st, routerContract, "get_factory", nil)
	if err != nil {
		return "", err
	}
	return benchmarkScValContractAddress(result)
}

func benchmarkSoroswapTokenBalance(ctx context.Context, st *state.State, tokenContract, ownerAddress string) (xdr.Int128Parts, error) {
	ownerVal, err := benchmarkAddressScVal(ownerAddress)
	if err != nil {
		return xdr.Int128Parts{}, fmt.Errorf("encode owner address %s: %w", ownerAddress, err)
	}
	result, err := benchmarkSimulateReadonlyContractCall(ctx, st, tokenContract, "balance", xdr.ScVec{ownerVal})
	if err != nil {
		return xdr.Int128Parts{}, err
	}
	balance, ok := result.GetI128()
	if !ok {
		return xdr.Int128Parts{}, fmt.Errorf("balance returned %s, want i128", result.Type.String())
	}
	return balance, nil
}

func benchmarkSimulateReadonlyContractCall(
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
		BaseFee:              benchmarkBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
	})
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("build read-only contract call: %w", err)
	}
	b64, err := tx.Base64()
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("marshal read-only contract call: %w", err)
	}
	simCtx, cancel := context.WithTimeout(ctx, soroswapReadOnlyCallTimeout)
	defer cancel()
	simResp, err := st.RPCClient.SimulateTransaction(simCtx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("simulate contract call: %w", err)
	}
	if simResp.Error != "" {
		return xdr.ScVal{}, fmt.Errorf("simulate contract call: %s", simResp.Error)
	}
	if len(simResp.Results) != 1 || simResp.Results[0].ReturnValueXDR == nil {
		return xdr.ScVal{}, fmt.Errorf("simulate contract call returned no value")
	}
	var result xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(*simResp.Results[0].ReturnValueXDR, &result); err != nil {
		return xdr.ScVal{}, fmt.Errorf("decode contract return value: %w", err)
	}
	return result, nil
}

func benchmarkAddressScVal(address string) (xdr.ScVal, error) {
	if accountID, err := xdr.AddressToAccountId(address); err == nil {
		return xdr.ScVal{
			Type:    xdr.ScValTypeScvAddress,
			Address: &xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &accountID},
		}, nil
	}
	contractID, err := ledger.DecodeContractID(address)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("decode address %s: not an account or contract address", address)
	}
	return xdr.ScVal{
		Type:    xdr.ScValTypeScvAddress,
		Address: &xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID},
	}, nil
}

func benchmarkScValContractAddress(value xdr.ScVal) (string, error) {
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

func soroswapPathScVal(tokenA, tokenB string) (xdr.ScVal, error) {
	path := xdr.ScVec{}
	for _, token := range []string{tokenA, tokenB} {
		val, err := benchmarkAddressScVal(token)
		if err != nil {
			return xdr.ScVal{}, err
		}
		path = append(path, val)
	}
	pathRef := &path
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &pathRef}, nil
}

func benchmarkI128ScVal(value int64) xdr.ScVal {
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(value)}}
}

func benchmarkU64ScVal(value uint64) xdr.ScVal {
	v := xdr.Uint64(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}
}

func rewriteInvokeContractAccount(invokeArgs xdr.InvokeContractArgs, oldAddress, newAddress string) (xdr.InvokeContractArgs, error) {
	args := make(xdr.ScVec, len(invokeArgs.Args))
	for i, arg := range invokeArgs.Args {
		rewritten, err := rewriteScValAccount(arg, oldAddress, newAddress)
		if err != nil {
			return xdr.InvokeContractArgs{}, err
		}
		args[i] = rewritten
	}
	invokeArgs.Args = args
	return invokeArgs, nil
}

func rewriteSorobanAuthEntriesAccount(entries []xdr.SorobanAuthorizationEntry, oldAddress, newAddress string) ([]xdr.SorobanAuthorizationEntry, error) {
	rewritten := make([]xdr.SorobanAuthorizationEntry, len(entries))
	for i, entry := range entries {
		invocation, err := rewriteAuthorizedInvocationAccount(entry.RootInvocation, oldAddress, newAddress)
		if err != nil {
			return nil, err
		}
		entry.RootInvocation = invocation
		rewritten[i] = entry
	}
	return rewritten, nil
}

func rewriteAuthorizedInvocationAccount(invocation xdr.SorobanAuthorizedInvocation, oldAddress, newAddress string) (xdr.SorobanAuthorizedInvocation, error) {
	fn, err := rewriteAuthorizedFunctionAccount(invocation.Function, oldAddress, newAddress)
	if err != nil {
		return xdr.SorobanAuthorizedInvocation{}, err
	}
	subs := make([]xdr.SorobanAuthorizedInvocation, len(invocation.SubInvocations))
	for i, sub := range invocation.SubInvocations {
		rewritten, err := rewriteAuthorizedInvocationAccount(sub, oldAddress, newAddress)
		if err != nil {
			return xdr.SorobanAuthorizedInvocation{}, err
		}
		subs[i] = rewritten
	}
	invocation.Function = fn
	invocation.SubInvocations = subs
	return invocation, nil
}

func rewriteAuthorizedFunctionAccount(fn xdr.SorobanAuthorizedFunction, oldAddress, newAddress string) (xdr.SorobanAuthorizedFunction, error) {
	if fn.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn.ContractFn == nil {
		return fn, nil
	}
	rewritten, err := rewriteInvokeContractAccount(*fn.ContractFn, oldAddress, newAddress)
	if err != nil {
		return xdr.SorobanAuthorizedFunction{}, err
	}
	fn.ContractFn = &rewritten
	return fn, nil
}

func rewriteFootprintAccount(footprint xdr.LedgerFootprint, oldAddress, newAddress string) (xdr.LedgerFootprint, error) {
	readOnly := make([]xdr.LedgerKey, len(footprint.ReadOnly))
	for i, key := range footprint.ReadOnly {
		rewritten, err := rewriteLedgerKeyAccount(key, oldAddress, newAddress)
		if err != nil {
			return xdr.LedgerFootprint{}, err
		}
		readOnly[i] = rewritten
	}
	readWrite := make([]xdr.LedgerKey, len(footprint.ReadWrite))
	for i, key := range footprint.ReadWrite {
		rewritten, err := rewriteLedgerKeyAccount(key, oldAddress, newAddress)
		if err != nil {
			return xdr.LedgerFootprint{}, err
		}
		readWrite[i] = rewritten
	}
	return xdr.LedgerFootprint{ReadOnly: readOnly, ReadWrite: readWrite}, nil
}

func rewriteLedgerKeyAccount(key xdr.LedgerKey, oldAddress, newAddress string) (xdr.LedgerKey, error) {
	switch key.Type {
	case xdr.LedgerEntryTypeAccount:
		if key.Account != nil && key.Account.AccountId.Address() == oldAddress {
			accountID, err := xdr.AddressToAccountId(newAddress)
			if err != nil {
				return xdr.LedgerKey{}, err
			}
			key.Account.AccountId = accountID
		}
	case xdr.LedgerEntryTypeTrustline:
		if key.TrustLine != nil && key.TrustLine.AccountId.Address() == oldAddress {
			accountID, err := xdr.AddressToAccountId(newAddress)
			if err != nil {
				return xdr.LedgerKey{}, err
			}
			key.TrustLine.AccountId = accountID
		}
	case xdr.LedgerEntryTypeContractData:
		if key.ContractData != nil {
			rewritten, err := rewriteScValAccount(key.ContractData.Key, oldAddress, newAddress)
			if err != nil {
				return xdr.LedgerKey{}, err
			}
			key.ContractData.Key = rewritten
		}
	}
	return key, nil
}

func rewriteScValAccount(value xdr.ScVal, oldAddress, newAddress string) (xdr.ScVal, error) {
	switch value.Type {
	case xdr.ScValTypeScvAddress:
		if value.Address == nil || value.Address.Type != xdr.ScAddressTypeScAddressTypeAccount || value.Address.AccountId == nil {
			return value, nil
		}
		if value.Address.AccountId.Address() != oldAddress {
			return value, nil
		}
		accountID, err := xdr.AddressToAccountId(newAddress)
		if err != nil {
			return xdr.ScVal{}, err
		}
		value.Address = &xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &accountID}
		return value, nil
	case xdr.ScValTypeScvVec:
		if value.Vec == nil || *value.Vec == nil {
			return value, nil
		}
		vec := **value.Vec
		rewrittenVec := make(xdr.ScVec, len(vec))
		for i, entry := range vec {
			rewritten, err := rewriteScValAccount(entry, oldAddress, newAddress)
			if err != nil {
				return xdr.ScVal{}, err
			}
			rewrittenVec[i] = rewritten
		}
		vecRef := &rewrittenVec
		value.Vec = &vecRef
		return value, nil
	case xdr.ScValTypeScvMap:
		if value.Map == nil || *value.Map == nil {
			return value, nil
		}
		m := **value.Map
		rewrittenMap := make(xdr.ScMap, len(m))
		for i, entry := range m {
			key, err := rewriteScValAccount(entry.Key, oldAddress, newAddress)
			if err != nil {
				return xdr.ScVal{}, err
			}
			val, err := rewriteScValAccount(entry.Val, oldAddress, newAddress)
			if err != nil {
				return xdr.ScVal{}, err
			}
			rewrittenMap[i] = xdr.ScMapEntry{Key: key, Val: val}
		}
		mapRef := &rewrittenMap
		value.Map = &mapRef
		return value, nil
	default:
		return value, nil
	}
}
