package setup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	ozTokenName   = "BenchToken"
	ozTokenSymbol = "BLT"

	// ozTokenInitialBalance is 1,000,000.0 units at 7 decimals, matching the
	// initial per-account funding used for the classic benchmark assets.
	ozTokenInitialBalance int64 = 10_000_000_000_000

	// ozTokenMintBatchSize controls how many participant accounts are funded by
	// one owner-authorized `mint_batch` call. On current testnet settings,
	// `tx_max_write_ledger_entries` is 50. Each recipient adds one balance
	// write and minting also updates total supply, so keep the batch below 49
	// recipients to leave a little safety margin for contract-side writes.
	ozTokenMintBatchSize    = 48
	ozBalanceCheckBatchSize = 100
)

var ozTokenWasmPaths = []string{
	"contracts/oz_token.wasm",
}

type ozTokenStep struct{}

func (ozTokenStep) Name() string { return "deploy OZ custom token" }

// Run ensures the deterministic OZ benchmark token contract exists and that
// every participant account has its intended OZ balance. On re-runs, this
// reconciles ledger state so partial mint failures are repaired.
func (ozTokenStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	logger = logger.WithField("phase", "oz token")

	contractID, contractIDStr, preimage, err := ozTokenContractIdentity(cfg.NetworkPassphrase, st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("derive OZ token contract ID: %w", err)
	}

	exists, err := ledger.ContractInstanceExists(ctx, st.RPCClient, contractID)
	if err != nil {
		return fmt.Errorf("check OZ token existence: %w", err)
	}

	deployedNow := false
	if !exists {
		wasmBytes, wasmPath, err := readOZTokenWasm()
		if err != nil {
			return err
		}
		logger.Infof("uploading Wasm from %s", wasmPath)

		wasmHash := xdr.Hash(sha256.Sum256(wasmBytes))
		if err := uploadOZTokenWasm(ctx, logger, st, cfg.NetworkPassphrase, wasmBytes); err != nil {
			return fmt.Errorf("upload OZ token Wasm: %w", err)
		}
		logger.Infof("uploaded Wasm hash %x", wasmHash)

		if err := deployOZTokenContract(ctx, logger, st, cfg.NetworkPassphrase, preimage, wasmHash); err != nil {
			return fmt.Errorf("deploy OZ token contract: %w", err)
		}
		deployedNow = true
	}

	logger.Infof("deployed contract at %s (existed=%t)", contractIDStr, !deployedNow)
	st.OZTokenContract = contractIDStr
	defer func() {
		st.PendingOZMintKPs = nil
	}()

	if len(st.AccountKPs) == 0 {
		logger.Info("no participant accounts to reconcile")
		return nil
	}

	mintTargets, err := accountsMissingOZBalances(ctx, st, contractID, st.AccountKPs)
	if err != nil {
		return fmt.Errorf("reconcile OZ balances: %w", err)
	}
	if len(mintTargets) == 0 {
		logger.Info("all participant accounts already have OZ balances")
		return nil
	}
	logger.Infof("minting OZ balances to %d/%d participant accounts", len(mintTargets), len(st.AccountKPs))

	if err := mintOZTokenBalances(ctx, logger, st, cfg.NetworkPassphrase, contractID, mintTargets); err != nil {
		return fmt.Errorf("mint OZ token balances: %w", err)
	}
	return nil
}

func readOZTokenWasm() ([]byte, string, error) {
	for _, path := range ozTokenWasmPaths {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, path, nil
		}
		if !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("read OZ token Wasm %q: %w", path, err)
		}
	}
	return nil, "", fmt.Errorf(
		"OZ token Wasm not found; run %s or place it at one of: %s",
		contractWasmRefreshScript,
		strings.Join(ozTokenWasmPaths, ", "),
	)
}

func ozTokenContractIdentity(networkPassphrase, deployerAddress string) (xdr.ContractId, string, xdr.ContractIdPreimage, error) {
	accountID, err := xdr.AddressToAccountId(deployerAddress)
	if err != nil {
		return xdr.ContractId{}, "", xdr.ContractIdPreimage{}, fmt.Errorf("parse deployer address: %w", err)
	}

	salt := ozTokenSalt()
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

func ozTokenSalt() xdr.Uint256 {
	return xdr.Uint256(sha256.Sum256([]byte("stellar-rpc-blaster/tx-load-test/oz-token/v1")))
}

func uploadOZTokenWasm(
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

func deployOZTokenContract(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	preimage xdr.ContractIdPreimage,
	wasmHash xdr.Hash,
) error {
	feePayerID, err := xdr.AddressToAccountId(st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("parse fee payer address: %w", err)
	}

	args := xdr.CreateContractArgsV2{
		ContractIdPreimage: preimage,
		Executable: xdr.ContractExecutable{
			Type:     xdr.ContractExecutableTypeContractExecutableWasm,
			WasmHash: &wasmHash,
		},
		ConstructorArgs: []xdr.ScVal{
			sharedsoroban.StringScVal(ozTokenName),
			sharedsoroban.StringScVal(ozTokenSymbol),
			{
				Type: xdr.ScValTypeScvAddress,
				Address: &xdr.ScAddress{
					Type:      xdr.ScAddressTypeScAddressTypeAccount,
					AccountId: &feePayerID,
				},
			},
		},
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:             xdr.HostFunctionTypeHostFunctionTypeCreateContractV2,
			CreateContractV2: &args,
		},
		Auth: []xdr.SorobanAuthorizationEntry{{
			Credentials: xdr.SorobanCredentials{
				Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
			},
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

func mintOZTokenBalances(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	contractID xdr.ContractId,
	accountKPs []*keypair.Full,
) error {
	contractAddress := xdr.ScAddress{
		Type:       xdr.ScAddressTypeScAddressTypeContract,
		ContractId: &contractID,
	}

	totalBatches := (len(accountKPs) + ozTokenMintBatchSize - 1) / ozTokenMintBatchSize
	for b := range totalBatches {
		start := b * ozTokenMintBatchSize
		end := min(start+ozTokenMintBatchSize, len(accountKPs))
		batch := accountKPs[start:end]

		recipients := make(xdr.ScVec, 0, len(batch))
		for _, kp := range batch {
			addrVal, err := sharedsoroban.AddressScVal(kp.Address())
			if err != nil {
				return fmt.Errorf("batch %d: encode recipient address %s: %w", b+1, kp.Address(), err)
			}
			recipients = append(recipients, addrVal)
		}
		recipientsRef := &recipients

		invokeArgs := xdr.InvokeContractArgs{
			ContractAddress: contractAddress,
			FunctionName:    "mint_batch",
			Args: xdr.ScVec{
				{Type: xdr.ScValTypeScvVec, Vec: &recipientsRef},
				sharedsoroban.I128ScVal(ozTokenInitialBalance),
			},
		}

		op := &txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &invokeArgs,
			},
			Auth:          sharedsoroban.SourceAccountContractAuth(invokeArgs),
			SourceAccount: st.FeePayerKP.Address(),
		}

		logger.Infof("mint batch %d/%d (%d accounts)", b+1, totalBatches, len(batch))
		if err := state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op); err != nil {
			return fmt.Errorf("batch %d: %w", b+1, err)
		}
	}
	return nil
}

func accountsMissingOZBalances(
	ctx context.Context,
	st *state.State,
	contractID xdr.ContractId,
	accountKPs []*keypair.Full,
) ([]*keypair.Full, error) {
	balances, err := ledger.FetchOZBalances(ctx, st.RPCClient, contractID, accountKPs, ozBalanceCheckBatchSize)
	if err != nil {
		return nil, err
	}

	missing := make([]*keypair.Full, 0)
	for _, kp := range accountKPs {
		balance, ok := balances[kp.Address()]
		if !ok || !ledger.HasPositiveI128(balance) {
			missing = append(missing, kp)
		}
	}

	return missing, nil
}
