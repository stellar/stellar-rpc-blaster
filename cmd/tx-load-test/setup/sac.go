package setup

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type sacStep struct{}

func (sacStep) Name() string { return "deploy SAC instances" }

// Run deploys a Stellar Asset Contract (SAC) wrapping each of the 3 benchmark
// classic assets. The SAC is the canonical Soroban token interface required by
// Soroswap pools.
//
// Each SAC contract ID is derived deterministically from the asset descriptor
// and the network passphrase, so it is idempotent: if the contract instance
// already exists on-ledger the step records the ID and moves on.
func (sacStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	for i, asset := range st.Assets {
		logger.Infof("sac[%d]: checking %s/%s", i, asset.Code, asset.Issuer)

		xdrAsset, err := asset.ToXDR()
		if err != nil {
			return fmt.Errorf("asset[%d] to XDR: %w", i, err)
		}

		contractIDBytes, err := xdrAsset.ContractID(cfg.NetworkPassphrase)
		if err != nil {
			return fmt.Errorf("asset[%d] derive SAC contract ID: %w", i, err)
		}
		contractID := xdr.ContractId(contractIDBytes)

		exists, err := contractInstanceExists(ctx, st.RPCClient, contractID)
		if err != nil {
			return fmt.Errorf("asset[%d] check SAC existence: %w", i, err)
		}

		if exists {
			logger.Infof("sac[%d]: already deployed, skipping", i)
		} else {
			logger.Infof("sac[%d]: deploying SAC for %s/%s", i, asset.Code, asset.Issuer)
			if err = deploySAC(ctx, logger, st, cfg.NetworkPassphrase, xdrAsset); err != nil {
				return fmt.Errorf("asset[%d] deploy SAC: %w", i, err)
			}
		}

		contractIDStr, err := strkey.Encode(strkey.VersionByteContract, contractID[:])
		if err != nil {
			return fmt.Errorf("asset[%d] encode SAC contract ID: %w", i, err)
		}
		st.SACs[i] = contractIDStr
		logger.Infof("sac[%d]: contract ID %s", i, contractIDStr)
	}

	return nil
}

// deploySAC submits a CreateContractV2 host-function transaction that wraps
// xdrAsset as a Stellar Asset Contract (SAC). The fee-payer account is used
// as both the transaction source and the Soroban auth credential (source-account
// auth is sufficient because the fee payer is the asset issuer in this setup).
//
// CreateContractV2 is used (rather than V1) for forward-compatibility; no
// constructor args are required for a SAC so ConstructorArgs is left nil.
func deploySAC(
	ctx context.Context,
	logger *log.Entry,
	st *state.State,
	networkPassphrase string,
	xdrAsset xdr.Asset,
) error {
	args := xdr.CreateContractArgsV2{
		ContractIdPreimage: xdr.ContractIdPreimage{
			Type:      xdr.ContractIdPreimageTypeContractIdPreimageFromAsset,
			FromAsset: &xdrAsset,
		},
		Executable: xdr.ContractExecutable{
			Type: xdr.ContractExecutableTypeContractExecutableStellarAsset,
		},
		// ConstructorArgs: nil  -- SACs need no constructor arguments.
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:             xdr.HostFunctionTypeHostFunctionTypeCreateContractV2,
			CreateContractV2: &args,
		},
		Auth: []xdr.SorobanAuthorizationEntry{
			{
				Credentials: xdr.SorobanCredentials{
					Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
				},
				RootInvocation: xdr.SorobanAuthorizedInvocation{
					Function: xdr.SorobanAuthorizedFunction{
						Type:                   xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeCreateContractV2HostFn,
						CreateContractV2HostFn: &args,
					},
				},
			},
		},
		SourceAccount: st.FeePayerKP.Address(),
	}

	return state.SubmitSorobanAndWait(ctx, logger, st.RPCClient, networkPassphrase, st.FeePayerKP, op)
}

// contractInstanceExists returns true when the contract instance ledger entry
// is already present on the network (i.e. the contract has been deployed
// before).
func contractInstanceExists(ctx context.Context, rpc interface {
	GetLedgerEntries(context.Context, protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
}, contractID xdr.ContractId) (bool, error) {
	instanceKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &contractID,
			},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}

	keyB64, err := xdr.MarshalBase64(instanceKey)
	if err != nil {
		return false, fmt.Errorf("marshal ledger key: %w", err)
	}

	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{keyB64}})
	if err != nil {
		return false, fmt.Errorf("get ledger entries: %w", err)
	}

	return len(resp.Entries) > 0, nil
}
