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

// MaxSACHolderAccounts caps how many participant accounts receive classic
// trustlines and initial SAC-transfer balances. The first N accounts are the
// active SAC holder subset.
const MaxSACHolderAccounts = 1000

// PersistedState is the JSON-serializable representation of everything
// produced by the setup phase and consumed by bench / teardown.
//
// No secrets are stored on disk. The fee-payer seed is represented by its
// SHA-256 hash so the state file can verify the correct seed is supplied at
// runtime (via FEE_PAYER). Participant account keypairs
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
	//
	// On disk this is stored compactly as AccountRanges; this field remains
	// the in-memory canonical form (populated by NewPersistedState from either
	// representation) and is accepted on read for legacy state files.
	AccountIndices []int `json:"account_indices,omitempty"`

	// AccountRanges is the compact on-disk encoding of AccountIndices: each
	// entry is a single index ("5") or an inclusive contiguous run ("1-4500"),
	// ascending and non-overlapping. Save always writes this form; a 4,500
	// account pool serializes as one short string instead of 4,500 array
	// elements. Ordering is canonicalized (sorted ascending, deduplicated).
	AccountRanges []string `json:"account_ranges,omitempty"`

	// SACHolderIndices are the derivation indices of the participant accounts
	// that hold classic trustlines and participate in SAC transfer benchmarks.
	// This is typically the first formula-derived holder-count accounts.
	// Stored on disk as SACHolderRanges; see AccountIndices.
	SACHolderIndices []int `json:"sac_holder_indices,omitempty"`

	// SACHolderRanges is the compact on-disk encoding of SACHolderIndices;
	// see AccountRanges.
	SACHolderRanges []string `json:"sac_holder_ranges,omitempty"`

	// Assets holds the 3 benchmark asset codes (issuer = fee payer).
	Assets [3]string `json:"assets"`

	// SACs holds the strkey-encoded contract IDs (C...) for each deployed SAC.
	SACs [3]string `json:"sacs"`

	// OZTokenContract holds the strkey-encoded contract ID (C...) of the
	// deployed OpenZeppelin benchmark token contract.
	OZTokenContract string `json:"oz_token_contract,omitempty"`

	// SoroswapFactoryContract holds the strkey-encoded contract ID (C...) of the
	// Soroswap factory contract used for benchmark pool management.
	SoroswapFactoryContract string `json:"soroswap_factory_contract,omitempty"`

	// SoroswapRouterContract holds the strkey-encoded contract ID (C...) of the
	// Soroswap router contract used for benchmark swap traffic.
	SoroswapRouterContract string `json:"soroswap_router_contract,omitempty"`

	// SoroswapPairContracts holds the pair contract IDs created or reused for the
	// benchmark Soroswap pools.
	SoroswapPairContracts []string `json:"soroswap_pair_contracts,omitempty"`

	// CleanedUp is set to true when teardown merges all accounts successfully.
	// A partial cleanup sets this to false and logs a suggestion to re-run
	// teardown.
	CleanedUp bool `json:"cleaned_up"`
}

// NewPersistedState reads a PersistedState from path. Both index encodings
// are accepted on read -- the compact range form written by current versions
// and the flat index arrays written by older ones -- and normalized so that
// AccountIndices / SACHolderIndices are always the populated, canonical
// in-memory representation.
func NewPersistedState(path string) (*PersistedState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var ps PersistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	if err := ps.normalizeIndexEncoding(); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	if err := ps.Validate(); err != nil {
		return nil, fmt.Errorf("validate state file: %w", err)
	}
	return &ps, nil
}

// normalizeIndexEncoding expands the on-disk range encoding into the in-memory
// flat index lists. A file carrying BOTH encodings for the same field is
// rejected rather than silently preferring one -- that only happens via hand
// editing, and guessing wrong would target the wrong accounts.
func (ps *PersistedState) normalizeIndexEncoding() error {
	if len(ps.AccountRanges) > 0 {
		if len(ps.AccountIndices) > 0 {
			return fmt.Errorf("state file has both account_ranges and account_indices; remove one")
		}
		indices, err := decodeIndexRanges(ps.AccountRanges)
		if err != nil {
			return fmt.Errorf("account_ranges: %w", err)
		}
		ps.AccountIndices = indices
		ps.AccountRanges = nil
	}
	if len(ps.SACHolderRanges) > 0 {
		if len(ps.SACHolderIndices) > 0 {
			return fmt.Errorf("state file has both sac_holder_ranges and sac_holder_indices; remove one")
		}
		indices, err := decodeIndexRanges(ps.SACHolderRanges)
		if err != nil {
			return fmt.Errorf("sac_holder_ranges: %w", err)
		}
		ps.SACHolderIndices = indices
		ps.SACHolderRanges = nil
	}
	return nil
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
// file atomically (write to .tmp then rename). Index lists are always
// serialized in the compact range form (account_ranges / sac_holder_ranges);
// the receiver itself is not mutated.
func (ps *PersistedState) Save(path string) error {
	out := *ps
	out.AccountRanges = encodeIndexRanges(ps.AccountIndices)
	out.AccountIndices = nil
	out.SACHolderRanges = encodeIndexRanges(ps.SACHolderIndices)
	out.SACHolderIndices = nil
	data, err := json.MarshalIndent(&out, "", "  ")
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

	// SACHolderKPs are the subset of participant accounts that have classic
	// trustlines and initial benchmark asset balances. SAC transfer benchmarks
	// use only this subset.
	SACHolderKPs []*keypair.Full

	// SACs holds the strkey-encoded contract ID (C...) of the deployed SAC for
	// each benchmark asset, in the same order as Assets.
	SACs [3]string

	// OZTokenContract holds the strkey-encoded contract ID (C...) of the
	// deployed OpenZeppelin benchmark token contract.
	OZTokenContract string

	// SoroswapFactoryContract holds the strkey-encoded contract ID (C...) of the
	// Soroswap factory contract used for benchmark pool management.
	SoroswapFactoryContract string

	// SoroswapRouterContract holds the strkey-encoded contract ID (C...) of the
	// Soroswap router contract used for benchmark swap traffic.
	SoroswapRouterContract string

	// SoroswapPairContracts holds the pair contract IDs created or reused for the
	// benchmark Soroswap pools.
	SoroswapPairContracts []string

	// PendingOZMintKPs is the transient account delta created by the current
	// setup run. It is retained only as a setup-time hint; OZ balance
	// reconciliation always verifies on-ledger balances before minting.
	PendingOZMintKPs []*keypair.Full
}

// SACHolderCount returns how many participant accounts should be active SAC
// holders for a given total account count.
func SACHolderCount(totalAccounts int) int {
	return min(totalAccounts, MaxSACHolderAccounts)
}

// DefaultSACHolderKPs returns the default SAC-active subset: the first
// min(len(accountKPs), 1000) participant accounts.
func DefaultSACHolderKPs(accountKPs []*keypair.Full) []*keypair.Full {
	n := SACHolderCount(len(accountKPs))
	if n == 0 {
		return nil
	}
	holders := make([]*keypair.Full, n)
	copy(holders, accountKPs[:n])
	return holders
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
		RPCURL:                  rpcURL,
		NetworkPassphrase:       s.NetworkPassphrase,
		FeePayerHash:            HashSeed(s.FeePayerKP.Seed()),
		SACs:                    s.SACs,
		OZTokenContract:         s.OZTokenContract,
		SoroswapFactoryContract: s.SoroswapFactoryContract,
		SoroswapRouterContract:  s.SoroswapRouterContract,
	}
	ps.SoroswapPairContracts = append([]string(nil), s.SoroswapPairContracts...)
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

	holderKPs := s.SACHolderKPs
	if len(holderKPs) == 0 && len(s.AccountKPs) > 0 {
		holderKPs = DefaultSACHolderKPs(s.AccountKPs)
	}
	ps.SACHolderIndices = make([]int, len(holderKPs))
	for i, kp := range holderKPs {
		idx, err := RecoverIndex(kp)
		if err != nil {
			return nil, fmt.Errorf("recover SAC holder index %d: %w", i, err)
		}
		ps.SACHolderIndices[i] = idx
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
				"(check FEE_PAYER)")
	}

	feePayerKP, err := keypair.ParseFull(feePayerSeed)
	if err != nil {
		return nil, fmt.Errorf("parse fee-payer seed: %w", err)
	}

	accountKPs := make([]*keypair.Full, len(ps.AccountIndices))
	accountByIndex := make(map[int]*keypair.Full, len(ps.AccountIndices))
	for i, idx := range ps.AccountIndices {
		kp, err := DeriveKeypair(feePayerKP, idx)
		if err != nil {
			return nil, fmt.Errorf("derive account %d (index %d): %w", i, idx, err)
		}
		accountKPs[i] = kp
		accountByIndex[idx] = kp
	}

	holderKPs := DefaultSACHolderKPs(accountKPs)
	if len(ps.SACHolderIndices) > 0 {
		holderKPs = make([]*keypair.Full, len(ps.SACHolderIndices))
		for i, idx := range ps.SACHolderIndices {
			kp, ok := accountByIndex[idx]
			if !ok {
				return nil, fmt.Errorf("sac holder index %d not found in account_indices", idx)
			}
			holderKPs[i] = kp
		}
	}

	var assets [3]txnbuild.CreditAsset
	for i, code := range ps.Assets {
		assets[i] = txnbuild.CreditAsset{Code: code, Issuer: feePayerKP.Address()}
	}

	return &State{
		RPCClient:               rpcclient.NewClient(rpcURL, nil),
		FeePayerKP:              feePayerKP,
		NetworkPassphrase:       ps.NetworkPassphrase,
		Assets:                  assets,
		AccountKPs:              accountKPs,
		SACHolderKPs:            holderKPs,
		SACs:                    ps.SACs,
		OZTokenContract:         ps.OZTokenContract,
		SoroswapFactoryContract: ps.SoroswapFactoryContract,
		SoroswapRouterContract:  ps.SoroswapRouterContract,
		SoroswapPairContracts:   append([]string(nil), ps.SoroswapPairContracts...),
	}, nil
}
