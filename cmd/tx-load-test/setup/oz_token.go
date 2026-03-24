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
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	ozTokenName   = "BenchToken"
	ozTokenSymbol = "BLT"

	// ozTokenInitialBalance is 1,000,000.0 units at 7 decimals, matching the
	// initial per-account funding used for the classic benchmark assets.
	ozTokenInitialBalance int64 = 10_000_000_000_000

	// ozTokenMintBatchSize controls how many participant accounts are funded by
	// one owner-authorized `mint_batch` call. Keep it below the likely
	// Soroban footprint ceiling because each recipient adds a balance entry and
	// minting also updates total supply.
	ozTokenMintBatchSize = 80
)

var ozTokenWasmPaths = []string{
	"contracts/oz_token.wasm",
	"contracts/oz_token/target/wasm32v1-none/release/oz_token_contract.wasm",
	"contracts/oz_token/target/wasm32-unknown-unknown/release/oz_token_contract.wasm",
}

type ozTokenStep struct{}

func (ozTokenStep) Name() string { return "deploy OZ custom token" }

// Run ensures the deterministic OZ benchmark token contract exists and mints
// balances to the account delta produced by the current setup run. On the
// first run, the contract is deployed and all participant accounts are funded.
func (ozTokenStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	logger = logger.WithField("phase", "oz token")

	contractID, contractIDStr, preimage, err := ozTokenContractIdentity(cfg.NetworkPassphrase, st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("derive OZ token contract ID: %w", err)
	}

	exists, err := contractInstanceExists(ctx, st.RPCClient, contractID)
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

	mintTargets := st.PendingOZMintKPs
	if deployedNow {
		mintTargets = st.AccountKPs
	}
	defer func() {
		st.PendingOZMintKPs = nil
	}()

	if len(mintTargets) == 0 {
		logger.Info("no new accounts to mint")
		return nil
	}

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
		"OZ token Wasm not found; build the contract first and place it at one of: %s",
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
			stringScVal(ozTokenName),
			stringScVal(ozTokenSymbol),
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
			addrVal, err := addressScVal(kp.Address())
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
				i128ScVal(ozTokenInitialBalance),
			},
		}

		op := &txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &invokeArgs,
			},
			Auth:          sourceAccountContractAuth(invokeArgs),
			SourceAccount: st.FeePayerKP.Address(),
		}

		logger.Infof("mint batch %d/%d (%d accounts)", b+1, totalBatches, len(batch))
		if err := state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op); err != nil {
			return fmt.Errorf("batch %d: %w", b+1, err)
		}
	}
	return nil
}

func sourceAccountContractAuth(invokeArgs xdr.InvokeContractArgs) []xdr.SorobanAuthorizationEntry {
	return []xdr.SorobanAuthorizationEntry{{
		Credentials: xdr.SorobanCredentials{
			Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
		},
		RootInvocation: xdr.SorobanAuthorizedInvocation{
			Function: xdr.SorobanAuthorizedFunction{
				Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
				ContractFn: &invokeArgs,
			},
		},
	}}
}

func stringScVal(s string) xdr.ScVal {
	str := xdr.ScString(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &str}
}

func i128ScVal(amount int64) xdr.ScVal {
	return xdr.ScVal{
		Type: xdr.ScValTypeScvI128,
		I128: &xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(amount)},
	}
}

func addressScVal(address string) (xdr.ScVal, error) {
	accountID, err := xdr.AddressToAccountId(address)
	if err != nil {
		return xdr.ScVal{}, err
	}
	return xdr.ScVal{
		Type: xdr.ScValTypeScvAddress,
		Address: &xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &accountID,
		},
	}, nil
}
