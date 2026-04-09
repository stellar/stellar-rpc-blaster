package benchmark

import (
	"context"
	"fmt"
	"strings"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const readinessBatchSize = 100

func benchmarkStateCountError(label string, need, have int, subject string) error {
	return fmt.Errorf("%s benchmark state incomplete: need at least %d %s, got %d -- rerun setup", label, need, pluralizeCountSubject(need, subject), have)
}

func benchmarkTargeterCountError(label string, need, have int, subject string) error {
	return fmt.Errorf("%s benchmark requires at least %d %s, got %d", label, need, pluralizeCountSubject(need, subject), have)
}

func benchmarkMissingContractIDError(label, contract string) error {
	return fmt.Errorf("%s benchmark state incomplete: %s contract ID is empty -- run setup first", label, contract)
}

func pluralizeCountSubject(count int, subject string) string {
	if count == 1 {
		return subject
	}
	return subject + "s"
}

func verifyTrustlineBalancesReady(ctx context.Context, st *state.State, accounts []*keypair.Full, label string) error {
	balances, err := ledger.FetchTrustlineBalances(ctx, st.RPCClient, st.Assets[:], accounts, readinessBatchSize)
	if err != nil {
		return fmt.Errorf("fetch %s holder trustlines: %w", label, err)
	}

	missingCount := 0
	examples := make([]string, 0, 5)
	for _, kp := range accounts {
		accountBalances := balances[kp.Address()]
		for _, asset := range st.Assets {
			balance, ok := accountBalances[asset.GetCode()]
			if ok && balance > 0 {
				continue
			}
			missingCount++
			if len(examples) < cap(examples) {
				reason := "missing trustline"
				if ok && balance == 0 {
					reason = "zero balance"
				}
				examples = append(examples, fmt.Sprintf("%s %s (%s)", kp.Address(), asset.GetCode(), reason))
			}
		}
	}
	if missingCount > 0 {
		return fmt.Errorf(
			"%s benchmark state incomplete: %d holder trustlines/balances missing or zero; examples: %s -- rerun setup",
			label, missingCount, formatExamples(examples),
		)
	}

	return nil
}

func requireReadyContract(ctx context.Context, st *state.State, label string, contractIDStr string) (xdr.ContractId, error) {
	if contractIDStr == "" {
		return xdr.ContractId{}, fmt.Errorf("%s contract ID is empty -- run setup first", label)
	}
	contractID, err := ledger.DecodeContractID(contractIDStr)
	if err != nil {
		return xdr.ContractId{}, fmt.Errorf("decode %s contract ID: %w", label, err)
	}
	exists, err := ledger.ContractInstanceExists(ctx, st.RPCClient, contractID)
	if err != nil {
		return xdr.ContractId{}, fmt.Errorf("check %s contract: %w", label, err)
	}
	if !exists {
		return xdr.ContractId{}, fmt.Errorf("%s contract %s is missing on-ledger -- rerun setup", label, contractIDStr)
	}
	return contractID, nil
}

func formatExamples(examples []string) string {
	if len(examples) == 0 {
		return ""
	}
	return strings.Join(examples, "; ")
}
