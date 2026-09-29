package setup

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroswap"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func ensureContractWasmUploaded(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	artifact soroswapCoreArtifact,
) error {
	exists, err := contractCodeExists(ctx, st.RPCClient, artifact.wasmHash)
	if err != nil {
		return err
	}
	if exists {
		logger.Infof("%s Wasm already uploaded (%x)", artifact.label, artifact.wasmHash)
		return nil
	}
	logger.Infof("uploading %s Wasm from %s", artifact.label, artifact.wasmPath)
	return uploadContractWasm(ctx, logger, st, networkPassphrase, artifact.wasm)
}

func ensureContractDeployed(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	artifact soroswapCoreArtifact,
) error {
	exists, err := ledger.ContractInstanceExists(ctx, st.RPCClient, artifact.contractID)
	if err != nil {
		return err
	}
	if exists {
		logger.Infof("%s already deployed at %s", artifact.label, artifact.contractIDStr)
		return nil
	}
	logger.Infof("deploying %s to %s", artifact.label, artifact.contractIDStr)
	return deployWasmContract(ctx, logger, st, networkPassphrase, artifact.preimage, artifact.wasmHash)
}

func ensureSoroswapFactoryInitialized(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	factoryContract string,
	pairWasmHash xdr.Hash,
) error {
	setter, err := soroswapFeeToSetter(ctx, st, factoryContract)
	if err == nil {
		if setter != st.FeePayerKP.Address() {
			return fmt.Errorf("factory %s is already initialized with fee_to_setter %s, want %s", factoryContract, setter, st.FeePayerKP.Address())
		}
		logger.Infof("Soroswap factory already initialized with fee_to_setter=%s", setter)
		return nil
	}

	setterVal, err := sharedsoroban.AddressScVal(st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("encode fee payer address: %w", err)
	}
	logger.Infof("initializing Soroswap factory %s", factoryContract)
	if err := invokeContractNoAuth(ctx, logger, st, networkPassphrase, factoryContract, "initialize", xdr.ScVec{setterVal, sharedsoroban.BytesScVal(pairWasmHash[:])}); err != nil {
		return err
	}

	setter, err = soroswapFeeToSetter(ctx, st, factoryContract)
	if err != nil {
		return fmt.Errorf("verify factory initialization: %w", err)
	}
	if setter != st.FeePayerKP.Address() {
		return fmt.Errorf("factory %s initialized with fee_to_setter %s, want %s", factoryContract, setter, st.FeePayerKP.Address())
	}
	return nil
}

func ensureSoroswapRouterInitialized(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	routerContract string,
	factoryContract string,
) error {
	reportedFactory, err := soroswap.GetFactory(ctx, st, routerContract)
	if err == nil {
		if reportedFactory != factoryContract {
			return fmt.Errorf("router %s is already initialized with factory %s, want %s", routerContract, reportedFactory, factoryContract)
		}
		logger.Infof("Soroswap router already initialized with factory=%s", factoryContract)
		return nil
	}

	factoryVal, err := sharedsoroban.AddressScVal(factoryContract)
	if err != nil {
		return fmt.Errorf("encode factory contract address: %w", err)
	}
	logger.Infof("initializing Soroswap router %s", routerContract)
	if err := invokeContractNoAuth(ctx, logger, st, networkPassphrase, routerContract, "initialize", xdr.ScVec{factoryVal}); err != nil {
		return err
	}

	reportedFactory, err = soroswap.GetFactory(ctx, st, routerContract)
	if err != nil {
		return fmt.Errorf("verify router initialization: %w", err)
	}
	if reportedFactory != factoryContract {
		return fmt.Errorf("router %s initialized with factory %s, want %s", routerContract, reportedFactory, factoryContract)
	}
	return nil
}

func soroswapFeeToSetter(ctx context.Context, st *state.State, factoryContract string) (string, error) {
	result, err := soroswap.SimulateReadonlyContractCall(ctx, st, factoryContract, "fee_to_setter", nil)
	if err != nil {
		return "", err
	}
	return sharedsoroban.ScValAccountAddress(result)
}

func uploadContractWasm(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	wasm []byte,
) error {
	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeUploadContractWasm,
			Wasm: &wasm,
		},
		SourceAccount: st.FeePayerKP.Address(),
	}
	return state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op)
}

func deployWasmContract(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	preimage xdr.ContractIdPreimage,
	wasmHash xdr.Hash,
) error {
	args := xdr.CreateContractArgsV2{
		ContractIdPreimage: preimage,
		Executable: xdr.ContractExecutable{
			Type:     xdr.ContractExecutableTypeContractExecutableWasm,
			WasmHash: &wasmHash,
		},
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:             xdr.HostFunctionTypeHostFunctionTypeCreateContractV2,
			CreateContractV2: &args,
		},
		Auth: []xdr.SorobanAuthorizationEntry{{
			Credentials: xdr.SorobanCredentials{Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount},
			RootInvocation: xdr.SorobanAuthorizedInvocation{
				Function: xdr.SorobanAuthorizedFunction{
					Type:                   xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeCreateContractV2HostFn,
					CreateContractV2HostFn: &args,
				},
			},
		}},
		SourceAccount: st.FeePayerKP.Address(),
	}

	return state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op)
}

func invokeContractNoAuth(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	contractIDStr string,
	functionName string,
	args xdr.ScVec,
) error {
	contractID, err := ledger.DecodeContractID(contractIDStr)
	if err != nil {
		return err
	}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		},
		FunctionName: xdr.ScSymbol(functionName),
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

func contractCodeExists(ctx context.Context, rpc interface {
	GetLedgerEntries(context.Context, protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
}, hash xdr.Hash) (bool, error) {
	key := xdr.LedgerKey{
		Type:         xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.LedgerKeyContractCode{Hash: hash},
	}

	keyB64, err := xdr.MarshalBase64(key)
	if err != nil {
		return false, fmt.Errorf("marshal contract code ledger key: %w", err)
	}

	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{keyB64}})
	if err != nil {
		return false, fmt.Errorf("get contract code ledger entry: %w", err)
	}

	return len(resp.Entries) > 0, nil
}
