package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type persistedState struct {
	FeePayerHash    string   `json:"fee_payer_hash"`
	AccountIndices  []uint32 `json:"account_indices"`
	OZTokenContract string   `json:"oz_token_contract"`
	CleanedUp       bool     `json:"cleaned_up"`
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: FEE_PAYER=S... %s <public-key[,public-key...]> <state-file>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(os.Getenv("FEE_PAYER"), flag.Arg(0), flag.Arg(1)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(seed string, publicKeysCSV string, stateFile string) error {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return fmt.Errorf("fee-payer seed is required: set FEE_PAYER")
	}

	publicKeys, err := parsePublicKeys(publicKeysCSV)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return fmt.Errorf("read state file %q: %w", stateFile, err)
	}

	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("parse state file %q: %w", stateFile, err)
	}
	if st.CleanedUp {
		return fmt.Errorf("state file is marked cleaned_up=true")
	}
	if st.OZTokenContract == "" {
		return fmt.Errorf("state file has no oz_token_contract")
	}
	if len(st.AccountIndices) == 0 {
		return fmt.Errorf("state file has no account_indices")
	}
	if got := state.HashSeed(seed); got != st.FeePayerHash {
		return fmt.Errorf("provided seed does not match fee_payer_hash in %s", stateFile)
	}

	feePayer, err := keypair.ParseFull(seed)
	if err != nil {
		return fmt.Errorf("parse fee-payer seed: %w", err)
	}

	seedsByPublicKey := make(map[string]string, len(st.AccountIndices))
	for _, index := range st.AccountIndices {
		kp, err := state.DeriveKeypair(feePayer, int(index))
		if err != nil {
			return fmt.Errorf("derive account index %d: %w", index, err)
		}
		seedsByPublicKey[kp.Address()] = kp.Seed()
	}

	missing := make([]string, 0)
	for _, publicKey := range publicKeys {
		if _, ok := seedsByPublicKey[publicKey]; !ok {
			missing = append(missing, publicKey)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("public key(s) are not part of account_indices in %s: %s", stateFile, strings.Join(missing, ","))
	}

	for _, publicKey := range publicKeys {
		fmt.Printf("%s: %s\n", publicKey, seedsByPublicKey[publicKey])
	}
	return nil
}

func parsePublicKeys(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	publicKeys := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		publicKey := strings.TrimSpace(part)
		if publicKey == "" {
			continue
		}
		if _, err := strkey.Decode(strkey.VersionByteAccountID, publicKey); err != nil {
			return nil, fmt.Errorf("invalid public key %q: %w", publicKey, err)
		}
		if _, ok := seen[publicKey]; ok {
			continue
		}
		seen[publicKey] = struct{}{}
		publicKeys = append(publicKeys, publicKey)
	}
	if len(publicKeys) == 0 {
		return nil, fmt.Errorf("at least one comma-separated public key is required")
	}
	return publicKeys, nil
}
