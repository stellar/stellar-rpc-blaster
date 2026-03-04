package parameters

import (
	"fmt"
	"math/rand/v2"

	"github.com/stellar/go-stellar-sdk/toid"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// Builds a list of params maps for a data-dependent endpoint,
// one per seed item, so the targeter can rotate through distinct request payloads.
func BuildEndpointParams(endpointKey string, params *Parameters) ([]map[string]any, error) {
	if needs, err := EndpointNeedsData(endpointKey); err != nil {
		return nil, err
	} else if !needs {
		return []map[string]any{{}}, nil
	}

	switch endpointKey {
	case "getTransaction":
		txs := params.Output.TxHashes
		var result []map[string]any
		for _, hash := range txs {
			result = append(result, map[string]any{
				"hash":   hash,
				"format": util.VaryFormat(),
			})
		}
		return result, nil

	case "getLedgerEntries":
		keys := params.Output.LedgerKeys
		count := min(len(keys), 100)
		result := make([]map[string]any, count)
		for i := range count {
			n := min(util.VaryKeyCount(), uint(len(keys)))
			// Pick n distinct random keys
			keys := util.ChooseNAtRandom(keys, int(n))
			result[i] = map[string]any{
				"keys":   keys,
				"format": util.VaryFormat(),
			}
		}
		return result, nil

	case "getTransactions":
		lr := params.Output.LedgerRange
		span := int(lr.Last - lr.First)
		if span <= 0 {
			return nil, fmt.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, 100)
		result := make([]map[string]any, count)
		for i := range count {
			start := lr.First + uint32(rand.IntN(span))
			limit := util.VaryLimit(uint(util.TxPageLimit))
			entry := map[string]any{
				"startLedger": start,
				"format":      util.VaryFormat(),
			}
			entry["pagination"] = setPaginationMap(limit, entry)
			result[i] = entry
		}
		return result, nil

	case "getLedgers":
		lr := params.Output.LedgerRange
		span := int(lr.Last - lr.First)
		if span <= 0 {
			return nil, fmt.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, 100)
		result := make([]map[string]any, count)
		for i := range count {
			start := lr.First + uint32(rand.IntN(span))
			limit := util.VaryLimit(uint(util.TxPageLimit))
			entry := map[string]any{
				"startLedger": start,
				"format":      util.VaryFormat(),
			}
			entry["pagination"] = setPaginationMap(limit, entry)
			result[i] = entry
		}
		return result, nil

	case "getEvents":
		return nil, fmt.Errorf("getEvents endpoint not yet implemented")

	default:
		return nil, fmt.Errorf("unsupported endpoint %s", endpointKey)
	}
}

// EndpointNeedsData reports whether the endpoint requires seed data to build requests.
func EndpointNeedsData(endpointKey string) (bool, error) {
	switch endpointKey {
	case "getTransaction", "getLedgerEntries", "getTransactions", "getLedgers", "getEvents", "simulateTransaction":
		return true, nil
	case "getHealth", "getNetwork", "getVersionInfo", "getLatestLedger", "getFeeStats":
		return false, nil
	default:
		return false, fmt.Errorf("unknown endpoint %q", endpointKey)
	}
}

func setPaginationMap(limit uint, entry map[string]any) map[string]any {
	cursor := ""
	if sl, ok := entry["startLedger"].(uint32); ok {
		tidVal := toid.New(int32(sl), 1, 1).ToInt64() // build valid TOID cursor from the startLedger already in the entry
		cursor = fmt.Sprintf("%019d-%010d", tidVal, 1)
	}
	pagination := util.VaryCursorBasedPagination(limit, cursor)
	paginationMap := map[string]any{"limit": pagination.Limit}
	if pagination.Cursor != "" {
		paginationMap["cursor"] = pagination.Cursor
		delete(entry, "startLedger") // cursor-based pagination omits startLedger
	}
	return paginationMap
}
