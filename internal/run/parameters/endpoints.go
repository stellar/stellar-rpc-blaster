package parameters

import (
	"math/rand/v2"

	"github.com/go-errors/errors"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// Builds a list of params maps for a data-dependent endpoint,
// one per seed item, so the targeter can rotate through distinct request payloads.
// TODO: make match the spec with more sophistocated parameter combinations
func BuildEndpointParams(endpointKey string, params *Parameters) ([]map[string]any, error) {
	switch endpointKey {
	case "getTransaction":
		tx := params.Output.TxHashes
		util.SetTxHashSuccessRatio(len(tx.Successes), len(tx.Failures))
		all := make([]string, 0, len(tx.Successes)+len(tx.Failures))
		all = append(all, tx.Successes...)
		all = append(all, tx.Failures...)
		var result []map[string]any
		for _, hash := range all {
			status := util.VaryTxStatus(util.TxHashSuccessRatio)
			switch status {
			case util.TxNotFound:
				// Use a made-up hash that won't be found
				result = append(result, map[string]any{
					"hash":   "0000000000000000000000000000000000000000000000000000000000000000",
					"format": util.VaryFormat(),
				})
			default:
				result = append(result, map[string]any{
					"hash":   hash,
					"format": util.VaryFormat(),
				})
			}
		}
		return result, nil

	case "getLedgerEntries":
		result := make([]map[string]any, len(params.Output.LedgerKeys))
		for i, key := range params.Output.LedgerKeys {
			result[i] = map[string]any{"keys": []string{key}}
		}
		return result, nil

	case "getTransactions":
		lr := params.Output.LedgerRange
		span := int(lr.Last - lr.First)
		if span <= 0 {
			return nil, errors.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, 100)
		result := make([]map[string]any, count)
		for i := range count {
			start := lr.First + uint32(rand.IntN(span))
			limit := util.VaryLimit(uint(util.TxPageLimit))
			pagination := util.VaryCursorBasedPagination(limit, "")
			entry := map[string]any{
				"startLedger": start,
				"format":      util.VaryFormat(),
			}
			paginationMap := map[string]any{"limit": pagination.Limit}
			if pagination.Cursor != "" {
				paginationMap["cursor"] = pagination.Cursor
				delete(entry, "startLedger") // cursor-based pagination omits startLedger
			}
			entry["pagination"] = paginationMap
			result[i] = entry
		}
		return result, nil

	case "getLedgers":
		lr := params.Output.LedgerRange
		span := int(lr.Last - lr.First)
		if span <= 0 {
			return nil, errors.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, 100)
		result := make([]map[string]any, count)
		for i := range count {
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
