// Package state defines the persisted and live state types shared across
// setup, bench, teardown, and sync phases.
package state

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
)

// DefaultStateFile is the default path for the persisted state file.
const DefaultStateFile = "state.json"

// AccountSeedStartIndex is the first DeriveKeypair index used for
// benchmark participant accounts. Indices 0 is reserved (identical to the
// fee-payer seed).
const AccountSeedStartIndex = 1

// PersistedState is the JSON-serializable representation of everything
// produced by the setup phase and consumed by bench / teardown.
//
// No secrets are stored on disk. The fee-payer seed is represented by its
// SHA-256 hash so the state file can verify the correct seed is supplied at
// runtime (via TX_LOAD_TEST_FEE_PAYER_SEED). Participant account keypairs
// are derived deterministically from the fee-payer seed + an integer index,
// so only the indices are persisted.
type PersistedState struct {
	// RPCURL is the RPC endpoint used during setup (recorded so bench /
	// teardown default to the same endpoint).
	RPCURL string `json:"rpc_url"`

	// NetworkPassphrase is the Stellar network passphrase confirmed during setup.
	NetworkPassphrase string `json:"network_passphrase"`

	// FeePayerHash is the hex-encoded SHA-256 hash of the fee-payer secret
	// key. Used to verify the seed supplied via environment variable.
	FeePayerHash string `json:"fee_payer_hash"`

	// AccountIndices are the DeriveKeypair indices of all participant
	// accounts. Typically contiguous (1..N), but may have gaps after sync
	// or partial teardown.
	AccountIndices []int `json:"account_indices"`

	// Assets holds the 3 benchmark asset codes (issuer = fee payer).
	Assets [3]string `json:"assets"`

	// SACs holds the strkey-encoded contract IDs (C...) for each deployed SAC.
	SACs [3]string `json:"sacs"`

	// CleanedUp is set to true when teardown merges all accounts successfully.
	// A partial cleanup sets this to false and logs a suggestion to re-run
	// teardown.
	CleanedUp bool `json:"cleaned_up"`
}

// NewPersistedState reads a PersistedState from path.
func NewPersistedState(path string) (*PersistedState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var ps PersistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	if err := ps.Validate(); err != nil {
		return nil, fmt.Errorf("validate state file: %w", err)
	}
	return &ps, nil
}

// Validate checks that the persisted state contains the minimum required
// fields for reconstruction and safe reuse.
func (ps *PersistedState) Validate() error {
	if ps == nil {
		return fmt.Errorf("nil persisted state")
	}
	if ps.RPCURL == "" {
		return fmt.Errorf("missing rpc_url")
	}
	if ps.NetworkPassphrase == "" {
		return fmt.Errorf("missing network_passphrase")
	}
	if ps.FeePayerHash == "" {
		return fmt.Errorf("missing fee_payer_hash")
	}
	return nil
}

// ValidateSetupConfig ensures a setup re-run uses the same network passphrase
// that was recorded in the state file.
func (ps *PersistedState) ValidateSetupConfig(networkPassphrase string) error {
	if err := ps.Validate(); err != nil {
		return err
	}
	if networkPassphrase != ps.NetworkPassphrase {
		return fmt.Errorf(
			"network passphrase mismatch: state file has %q but setup was given %q",
			ps.NetworkPassphrase, networkPassphrase,
		)
	}
	return nil
}

// ValidateRPCNetwork ensures the chosen RPC endpoint reports the same network
// passphrase recorded in the state file. This allows different RPC URLs to be
// used across phases while still preventing cross-network reuse.
func (ps *PersistedState) ValidateRPCNetwork(ctx context.Context, rpcURL string) error {
	if err := ps.Validate(); err != nil {
		return err
	}
	if rpcURL == "" {
		rpcURL = ps.RPCURL
	}
	rpc := rpcclient.NewClient(rpcURL, nil)
	netInfo, err := rpc.GetNetwork(ctx)
	if err != nil {
		return fmt.Errorf("get network from %q: %w", rpcURL, err)
	}
	if netInfo.Passphrase != ps.NetworkPassphrase {
		return fmt.Errorf(
			"state file network mismatch: rpc %q reports %q but state file stores %q",
			rpcURL, netInfo.Passphrase, ps.NetworkPassphrase,
		)
	}
	return nil
}

// Save writes ps to path as indented JSON, creating or overwriting the
// file atomically (write to .tmp then rename).
func (ps *PersistedState) Save(path string) error {
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}

// State holds the live runtime objects built from PersistedState. Fields that
// cannot be serialized (RPC client, parsed keypairs, typed assets) live here.
type State struct {
	// RPCClient is a shared RPC client initialized during the fee-payer step.
	RPCClient *rpcclient.Client

	// FeePayerKP is used to sign and fund all setup transactions.
	FeePayerKP *keypair.Full

	// NetworkPassphrase is the Stellar network passphrase.
	NetworkPassphrase string

	// Assets holds the 3 benchmark classic assets.
	Assets [3]txnbuild.CreditAsset

	// AccountKPs are the keypairs of the benchmark participant accounts.
	AccountKPs []*keypair.Full

	// SACs holds the strkey-encoded contract ID (C...) of the deployed SAC for
	// each benchmark asset, in the same order as Assets.
	SACs [3]string
}

// HashSeed returns the hex-encoded SHA-256 hash of a Stellar secret seed.
func HashSeed(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:])
}

// RecoverIndex extracts the DeriveKeypair derivation index from a derived
// keypair by reading the big-endian uint32 stored in bytes 28-31 of the raw
// seed. This works because DeriveKeypair overwrites exactly those bytes.
func RecoverIndex(kp *keypair.Full) (int, error) {
	if kp == nil {
		return 0, fmt.Errorf("nil keypair")
	}
	raw, err := strkey.Decode(strkey.VersionByteSeed, kp.Seed())
	if err != nil {
		return 0, fmt.Errorf("decode seed: %w", err)
	}
	return int(binary.BigEndian.Uint32(raw[28:])), nil
}

// ToPersistedState converts a live State into a PersistedState suitable for
// JSON serialization. No secrets are written -- only the hash of the
// fee-payer seed and the derivation indices recovered from each keypair.
func (s *State) ToPersistedState(rpcURL string) (*PersistedState, error) {
	if s == nil || s.FeePayerKP == nil {
		return nil, fmt.Errorf("state missing fee payer keypair")
	}

	ps := &PersistedState{
		RPCURL:            rpcURL,
		NetworkPassphrase: s.NetworkPassphrase,
		FeePayerHash:      HashSeed(s.FeePayerKP.Seed()),
		SACs:              s.SACs,
	}
	ps.AccountIndices = make([]int, len(s.AccountKPs))
	for i, kp := range s.AccountKPs {
		idx, err := RecoverIndex(kp)
		if err != nil {
			return nil, fmt.Errorf("recover account index %d: %w", i, err)
		}
		ps.AccountIndices[i] = idx
	}
	for i, a := range s.Assets {
		ps.Assets[i] = a.Code
	}
	return ps, nil
}

// FromPersistedState reconstructs a live State from a PersistedState.
// The fee-payer seed must be supplied at runtime (from the environment); it is
// verified against the stored hash before use. If rpcURL is empty, the URL
// recorded in the state file is used.
func FromPersistedState(ps *PersistedState, feePayerSeed, rpcURL string) (*State, error) {
	if err := ps.Validate(); err != nil {
		return nil, err
	}
	if rpcURL == "" {
		rpcURL = ps.RPCURL
	}
	if HashSeed(feePayerSeed) != ps.FeePayerHash {
		return nil, fmt.Errorf(
			"fee-payer seed does not match the hash stored in the state file " +
				"(check TX_LOAD_TEST_FEE_PAYER_SEED)")
	}

	feePayerKP, err := keypair.ParseFull(feePayerSeed)
	if err != nil {
		return nil, fmt.Errorf("parse fee-payer seed: %w", err)
	}

	accountKPs := make([]*keypair.Full, len(ps.AccountIndices))
	for i, idx := range ps.AccountIndices {
		kp, err := DeriveKeypair(feePayerKP, idx)
		if err != nil {
			return nil, fmt.Errorf("derive account %d (index %d): %w", i, idx, err)
		}
		accountKPs[i] = kp
	}

	var assets [3]txnbuild.CreditAsset
	for i, code := range ps.Assets {
		assets[i] = txnbuild.CreditAsset{Code: code, Issuer: feePayerKP.Address()}
	}

	return &State{
		RPCClient:         rpcclient.NewClient(rpcURL, nil),
		FeePayerKP:        feePayerKP,
		NetworkPassphrase: ps.NetworkPassphrase,
		Assets:            assets,
		AccountKPs:        accountKPs,
		SACs:              ps.SACs,
	}, nil
}
