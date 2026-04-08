package setup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const soroswapStandaloneNetworkPassphrase = "Standalone Network ; February 2017"

var soroswapPairWasmPaths = []string{
	"contracts/soroswap/contracts/pair/target/wasm32v1-none/release/soroswap_pair.wasm",
	"contracts/soroswap/contracts/pair/target/wasm32-unknown-unknown/release/soroswap_pair.wasm",
}

var soroswapFactoryWasmPaths = []string{
	"contracts/soroswap/contracts/factory/target/wasm32v1-none/release/soroswap_factory.wasm",
	"contracts/soroswap/contracts/factory/target/wasm32-unknown-unknown/release/soroswap_factory.wasm",
}

var soroswapRouterWasmPaths = []string{
	"contracts/soroswap/contracts/router/target/wasm32v1-none/release/soroswap_router.wasm",
	"contracts/soroswap/contracts/router/target/wasm32-unknown-unknown/release/soroswap_router.wasm",
}

type soroswapCoreStep struct{}

func (soroswapCoreStep) Name() string { return "deploy Soroswap core" }

func (soroswapCoreStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	factoryContract, routerContract := resolvedSoroswapContracts(cfg, st)
	if (factoryContract == "") != (routerContract == "") {
		return fmt.Errorf("soroswap factory/router configuration is incomplete")
	}
	if factoryContract != "" && routerContract != "" {
		st.SoroswapFactoryContract = factoryContract
		st.SoroswapRouterContract = routerContract
		logger.Infof("using existing Soroswap core: factory=%s router=%s", factoryContract, routerContract)
		return nil
	}
	if !SupportsSoroswapAutoBootstrap(cfg.NetworkPassphrase) {
		return fmt.Errorf("soroswap core contract IDs must be supplied on this network")
	}

	factoryContract, routerContract, err := ensureSoroswapCore(ctx, logger, cfg, st)
	if err != nil {
		return err
	}
	st.SoroswapFactoryContract = factoryContract
	st.SoroswapRouterContract = routerContract
	return nil
}

func SupportsSoroswapAutoBootstrap(networkPassphrase string) bool {
	switch networkPassphrase {
	case soroswapStandaloneNetworkPassphrase, network.FutureNetworkPassphrase:
		return true
	default:
		return false
	}
}

func ensureSoroswapCore(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
) (string, string, error) {
	pairWasm, pairWasmPath, err := readSoroswapWasm("Soroswap pair", soroswapPairWasmPaths)
	if err != nil {
		return "", "", err
	}
	factoryWasm, factoryWasmPath, err := readSoroswapWasm("Soroswap factory", soroswapFactoryWasmPaths)
	if err != nil {
		return "", "", err
	}
	routerWasm, routerWasmPath, err := readSoroswapWasm("Soroswap router", soroswapRouterWasmPaths)
	if err != nil {
		return "", "", err
	}

	pairWasmHash := xdr.Hash(sha256.Sum256(pairWasm))
	factoryWasmHash := xdr.Hash(sha256.Sum256(factoryWasm))
	routerWasmHash := xdr.Hash(sha256.Sum256(routerWasm))

	if err := ensureContractWasmUploaded(ctx, logger, st, cfg.NetworkPassphrase, "Soroswap pair", pairWasmPath, pairWasm, pairWasmHash); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap pair Wasm upload: %w", err)
	}
	if err := ensureContractWasmUploaded(ctx, logger, st, cfg.NetworkPassphrase, "Soroswap factory", factoryWasmPath, factoryWasm, factoryWasmHash); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap factory Wasm upload: %w", err)
	}
	if err := ensureContractWasmUploaded(ctx, logger, st, cfg.NetworkPassphrase, "Soroswap router", routerWasmPath, routerWasm, routerWasmHash); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap router Wasm upload: %w", err)
	}

	factoryID, factoryContract, factoryPreimage, err := deterministicContractIdentity(cfg.NetworkPassphrase, st.FeePayerKP.Address(), soroswapFactorySalt())
	if err != nil {
		return "", "", fmt.Errorf("derive Soroswap factory contract ID: %w", err)
	}
	routerID, routerContract, routerPreimage, err := deterministicContractIdentity(cfg.NetworkPassphrase, st.FeePayerKP.Address(), soroswapRouterSalt())
	if err != nil {
		return "", "", fmt.Errorf("derive Soroswap router contract ID: %w", err)
	}

	if err := ensureContractDeployed(ctx, logger, st, cfg.NetworkPassphrase, "Soroswap factory", factoryID, factoryContract, factoryPreimage, factoryWasmHash); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap factory deployment: %w", err)
	}
	if err := ensureSoroswapFactoryInitialized(ctx, logger, st, cfg.NetworkPassphrase, factoryContract, pairWasmHash); err != nil {
		return "", "", fmt.Errorf("initialize Soroswap factory: %w", err)
	}

	if err := ensureContractDeployed(ctx, logger, st, cfg.NetworkPassphrase, "Soroswap router", routerID, routerContract, routerPreimage, routerWasmHash); err != nil {
		return "", "", fmt.Errorf("ensure Soroswap router deployment: %w", err)
	}
	if err := ensureSoroswapRouterInitialized(ctx, logger, st, cfg.NetworkPassphrase, routerContract, factoryContract); err != nil {
		return "", "", fmt.Errorf("initialize Soroswap router: %w", err)
	}

	logger.Infof("Soroswap core ready: factory=%s router=%s", factoryContract, routerContract)
	return factoryContract, routerContract, nil
}

func readSoroswapWasm(label string, paths []string) ([]byte, string, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, path, nil
		}
		if !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("read %s Wasm %q: %w", label, path, err)
		}
	}
	return nil, "", fmt.Errorf("%s Wasm not found; build it first and place it at one of: %s", label, strings.Join(paths, ", "))
}

func ensureContractWasmUploaded(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	label string,
	wasmPath string,
	wasm []byte,
	wasmHash xdr.Hash,
) error {
	exists, err := contractCodeExists(ctx, st.RPCClient, wasmHash)
	if err != nil {
		return err
	}
	if exists {
		logger.Infof("%s Wasm already uploaded (%x)", label, wasmHash)
		return nil
	}
	logger.Infof("uploading %s Wasm from %s", label, wasmPath)
	return uploadContractWasm(ctx, logger, st, networkPassphrase, wasm)
}

func ensureContractDeployed(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	label string,
	contractID xdr.ContractId,
	contractIDStr string,
	preimage xdr.ContractIdPreimage,
	wasmHash xdr.Hash,
) error {
	exists, err := contractInstanceExists(ctx, st.RPCClient, contractID)
	if err != nil {
		return err
	}
	if exists {
		logger.Infof("%s already deployed at %s", label, contractIDStr)
		return nil
	}
	logger.Infof("deploying %s to %s", label, contractIDStr)
	return deployWasmContract(ctx, logger, st, networkPassphrase, preimage, wasmHash)
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

	setterVal, err := addressScVal(st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("encode fee payer address: %w", err)
	}
	logger.Infof("initializing Soroswap factory %s", factoryContract)
	if err := invokeContractNoAuth(ctx, logger, st, networkPassphrase, factoryContract, "initialize", xdr.ScVec{setterVal, bytesScVal(pairWasmHash[:])}); err != nil {
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
	reportedFactory, err := soroswapGetFactory(ctx, st, routerContract)
	if err == nil {
		if reportedFactory != factoryContract {
			return fmt.Errorf("router %s is already initialized with factory %s, want %s", routerContract, reportedFactory, factoryContract)
		}
		logger.Infof("Soroswap router already initialized with factory=%s", factoryContract)
		return nil
	}

	factoryVal, err := addressScVal(factoryContract)
	if err != nil {
		return fmt.Errorf("encode factory contract address: %w", err)
	}
	logger.Infof("initializing Soroswap router %s", routerContract)
	if err := invokeContractNoAuth(ctx, logger, st, networkPassphrase, routerContract, "initialize", xdr.ScVec{factoryVal}); err != nil {
		return err
	}

	reportedFactory, err = soroswapGetFactory(ctx, st, routerContract)
	if err != nil {
		return fmt.Errorf("verify router initialization: %w", err)
	}
	if reportedFactory != factoryContract {
		return fmt.Errorf("router %s initialized with factory %s, want %s", routerContract, reportedFactory, factoryContract)
	}
	return nil
}

func soroswapFeeToSetter(ctx context.Context, st *state.State, factoryContract string) (string, error) {
	result, err := simulateReadonlyContractCall(ctx, st, factoryContract, "fee_to_setter", nil)
	if err != nil {
		return "", err
	}
	return scValAccountAddress(result)
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
	contractID, err := decodeContractID(contractIDStr)
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

func deterministicContractIdentity(
	networkPassphrase string,
	deployerAddress string,
	salt xdr.Uint256,
) (xdr.ContractId, string, xdr.ContractIdPreimage, error) {
	accountID, err := xdr.AddressToAccountId(deployerAddress)
	if err != nil {
		return xdr.ContractId{}, "", xdr.ContractIdPreimage{}, fmt.Errorf("parse deployer address: %w", err)
	}

	preimage := xdr.ContractIdPreimage{
		Type: xdr.ContractIdPreimageTypeContractIdPreimageFromAddress,
		FromAddress: &xdr.ContractIdPreimageFromAddress{
			Address: xdr.ScAddress{
				Type:      xdr.ScAddressTypeScAddressTypeAccount,
				AccountId: &accountID,
			},
			Salt: salt,
		},
	}

	networkID := xdr.Hash(sha256.Sum256([]byte(networkPassphrase)))
	hashPreimage := xdr.HashIdPreimage{
		Type: xdr.EnvelopeTypeEnvelopeTypeContractId,
		ContractId: &xdr.HashIdPreimageContractId{
			NetworkId:          networkID,
			ContractIdPreimage: preimage,
		},
	}
	preimageBytes, err := hashPreimage.MarshalBinary()
	if err != nil {
		return xdr.ContractId{}, "", xdr.ContractIdPreimage{}, fmt.Errorf("marshal contract ID preimage: %w", err)
	}

	contractID := xdr.ContractId(sha256.Sum256(preimageBytes))
	contractIDStr, err := strkey.Encode(strkey.VersionByteContract, contractID[:])
	if err != nil {
		return xdr.ContractId{}, "", xdr.ContractIdPreimage{}, fmt.Errorf("encode contract ID: %w", err)
	}
	return contractID, contractIDStr, preimage, nil
}

func soroswapFactorySalt() xdr.Uint256 {
	return xdr.Uint256(sha256.Sum256([]byte("stellar-rpc-blaster/tx-load-test/soroswap/factory/v1")))
}

func soroswapRouterSalt() xdr.Uint256 {
	return xdr.Uint256(sha256.Sum256([]byte("stellar-rpc-blaster/tx-load-test/soroswap/router/v1")))
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

func bytesScVal(value []byte) xdr.ScVal {
	b := xdr.ScBytes(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}
}
