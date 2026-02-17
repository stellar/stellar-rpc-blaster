package parameters

import (
	"math/rand/v2"

	"github.com/go-errors/errors"

	"github.com/stellar/go-stellar-sdk/support/collections/set"
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
		keys := params.Output.LedgerKeys
		count := min(len(keys), 100)
		result := make([]map[string]any, count)
		for i := range count {
			n := util.VaryKeyCount()
			n = min(n, uint(len(keys)))

			// Pick n distinct random keys
			chosen := set.NewSet[string](int(n))
			for len(chosen) < int(n) {
				chosen.Add(keys[rand.IntN(len(keys))])
			}
			result[i] = map[string]any{
				"keys":   chosen.Slice(),
				"format": util.VaryFormat(),
			}
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
			limit := util.VaryLimit(uint(util.TxPageLimit))
			pagination := util.VaryCursorBasedPagination(limit, "")
			entry := map[string]any{
				"startLedger": start,
				"format":      util.VaryFormat(),
			}
			paginationMap := map[string]any{"limit": pagination.Limit}
			if pagination.Cursor != "" {
				paginationMap["cursor"] = pagination.Cursor
				delete(entry, "startLedger")
			}
			entry["pagination"] = paginationMap
			result[i] = entry
		}
		return result, nil

	case "getEvents":
		lr := params.Output.LedgerRange
		span := int(lr.Last - lr.First)
		if span <= 0 {
			return nil, errors.Errorf("empty ledger range for %s", endpointKey)
		}
		contractIDs := params.Output.ContractIDs
		topics := params.Output.EventTopics
		count := min(span, 100)
		result := make([]map[string]any, count)
		for i := range count {
			// Random recent window of 100-10000 ledgers, placed randomly within the seeded range
			maxWindow := min(span, 10000)
			window := 100 + rand.IntN(maxWindow-100+1)
			// Random start position that fits the window
			earliest := int(lr.First)
			latest := int(lr.Last) - window
			if latest < earliest {
				latest = earliest
			}
			start := uint32(earliest + rand.IntN(latest-earliest+1))
			endLedger := start + uint32(window)

			filter := util.VaryEventFilter(contractIDs, topics)

			entry := map[string]any{
				"startLedger": start,
				"endLedger":   endLedger,
				"filters":     []map[string]any{filter},
				"pagination":  map[string]any{"limit": util.VaryLimit(uint(util.EventsPageLimit))},
			}
			result[i] = entry
		}
		return result, nil

	default:
		return nil, errors.Errorf("endpoint %q does not support data-dependent parameters", endpointKey)
	}
}

// EndpointNeedsData reports whether the endpoint requires seed data to build requests.
func EndpointNeedsData(endpointKey string) bool {
	switch endpointKey {
	case "getTransaction", "getLedgerEntries", "getTransactions", "getLedgers", "getEvents":
		return true
	}
	return false
}
