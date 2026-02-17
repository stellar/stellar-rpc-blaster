package parameters

import (
	"math/rand/v2"

	"github.com/go-errors/errors"
)

// Builds a list of params maps for a data-dependent endpoint,
// one per seed item, so the targeter can rotate through distinct request payloads.
// TODO: make match the spec with more sophistocated parameter combinations
func BuildEndpointParams(endpointKey string, params *Parameters) ([]map[string]any, error) {
	switch endpointKey {
	case "getTransaction":
		tx := params.Output.TxHashes
		all := make([]string, 0, len(tx.Successes)+len(tx.Failures))
		all = append(all, tx.Successes...)
		all = append(all, tx.Failures...)
		result := make([]map[string]any, len(all))
		for i, hash := range all {
			result[i] = map[string]any{"hash": hash}
		}
		return result, nil

	case "getLedgerEntries":
		result := make([]map[string]any, len(params.Output.LedgerKeys))
		for i, key := range params.Output.LedgerKeys {
			result[i] = map[string]any{"keys": []string{key}}
		}
		return result, nil

	case "getTransactions", "getLedgers":
		lr := params.Output.LedgerRange
		span := int(lr.Last - lr.First)
		if span <= 0 {
			return nil, errors.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, 100) // up to 100 variant start ledgers
		result := make([]map[string]any, count)
		for i := range count {
			_ = i
			start := lr.First + uint32(rand.IntN(span))
			result[i] = map[string]any{"startLedger": start}
		}
		return result, nil

	default:
		return nil, errors.Errorf("endpoint %q does not support data-dependent parameters", endpointKey)
	}
}

// EndpointNeedsData reports whether the endpoint requires seed data to build requests.
func EndpointNeedsData(endpointKey string) bool {
	switch endpointKey {
	case "getTransaction", "getLedgerEntries", "getTransactions", "getLedgers":
		return true
	}
	return false
}
