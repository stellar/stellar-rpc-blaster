package parameters

import (
	"fmt"
	"math/rand/v2"

	"github.com/stellar/go-stellar-sdk/toid"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

// Builds a list of params maps for a data-dependent endpoint to vary request payloads.
func BuildEndpointParams(endpointKey string, maxNeededNumBodies int, params *Parameters, limit uint32) ([]map[string]any, error) {
	if needs, err := EndpointNeedsData(endpointKey); err != nil {
		return nil, err
	} else if !needs {
		return []map[string]any{{}}, nil
	}

	switch endpointKey {
	case "getTransaction":
		count := min(len(params.Output.TxHashes), maxNeededNumBodies, util.MaxNumPrebuiltBodies)
		result := make([]map[string]any, count)
		for i, hash := range params.Output.TxHashes[:count] {
			result[i] = map[string]any{
				"hash":      hash,
				"xdrFormat": util.VaryFormat(),
			}
		}
		return result, nil

	case "getLedgerEntries":
		keys := params.Output.LedgerKeys
		count := min(len(keys), maxNeededNumBodies, util.MaxNumPrebuiltBodies)
		result := make([]map[string]any, count)
		for i := range count {
			n := min(util.VaryKeyCount(), uint(len(keys)))
			// Pick n distinct random keys
			keys := util.ChooseNAtRandom(keys, int(n))
			result[i] = map[string]any{
				"keys":      keys,
				"xdrFormat": util.VaryFormat(),
			}
		}
		return result, nil

	case "getTransactions":
		lr := params.Output.LedgerRange
		span := int(lr.Last) - int(lr.First)
		if span <= 0 {
			return nil, fmt.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, maxNeededNumBodies, util.MaxNumPrebuiltBodies)

		result := make([]map[string]any, count)
		for i := range count {
			start := lr.First + uint32(rand.IntN(span))
			remaining := lr.Last - start
			entry := map[string]any{
				"startLedger": start,
				"xdrFormat":   util.VaryFormat(),
			}
			entry["pagination"] = setPaginationMap(start, remaining, entry, endpointKey, limit)
			result[i] = entry
		}
		return result, nil

	case "getLedgers":
		lr := params.Output.LedgerRange
		span := int(lr.Last) - int(lr.First)
		if span <= 0 {
			return nil, fmt.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, maxNeededNumBodies, util.MaxNumPrebuiltBodies)

		result := make([]map[string]any, count)
		for i := range count {
			start := lr.First + uint32(rand.IntN(span))
			remaining := lr.Last - start
			entry := map[string]any{
				"startLedger": start,
				"xdrFormat":   util.VaryFormat(),
			}
			entry["pagination"] = setPaginationMap(start, remaining, entry, endpointKey, limit)
			result[i] = entry
		}
		return result, nil

	case "getEvents":
		lr := params.Output.LedgerRange
		if lr.Last <= lr.First {
			return nil, fmt.Errorf("empty ledger range for %s", endpointKey)
		}
		lr.Last++ // treat as inclusive bound for getEvents
		span := lr.Last - lr.First
		eventBodies := params.Output.ContractEventData
		count := min(maxNeededNumBodies, util.MaxNumPrebuiltBodies)

		result := make([]map[string]any, count)
		for i := range count {
			filters := eventBodies.BuildEventsFilters()
			start := lr.First + uint32(rand.IntN(int(span)))
			end := min(start+limit, lr.Last)
			remaining := lr.Last - start
			entry := map[string]any{
				"startLedger": start,
				"endLedger":   end,
				"xdrFormat":   util.VaryFormat(),
				"filters":     []map[string]any{filters},
			}
			entry["pagination"] = setPaginationMap(start, remaining, entry, endpointKey, limit)
			result[i] = entry
		}
		return result, nil
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

func setPaginationMap(start uint32, remaining uint32, entry map[string]any, endpoint string, limit uint32) map[string]any {
	reqLimit := uint(min(limit, remaining))
	switch endpoint {
	case "getTransactions":
		cursor := toid.New(int32(start), 1, 1).String() // getTransactions cursor is a TOID
		return setPaginationMapWithCursor(reqLimit, cursor, entry)
	case "getLedgers":
		cursor := fmt.Sprintf("%d", start) // getLedgers cursor is a ledger sequence number
		return setPaginationMapWithCursor(reqLimit, cursor, entry)
	case "getEvents":
		return setPaginationMapForEvents(start, reqLimit, entry)
	default:
		return map[string]any{"limit": reqLimit}
	}
}

func setPaginationMapWithCursor(limit uint, cursor string, entry map[string]any) map[string]any {
	pagination := util.VaryCursorBasedPagination(limit, cursor)
	paginationMap := map[string]any{"limit": pagination.Limit}
	if pagination.Cursor != "" {
		paginationMap["cursor"] = pagination.Cursor
		delete(entry, "startLedger") // cursor-based pagination omits startLedger
	}
	return paginationMap
}

func setPaginationMapForEvents(start uint32, limit uint, entry map[string]any) map[string]any {
	cursor := fmt.Sprintf("%019d-%010d",
		toid.New(int32(start), 1, 1).ToInt64(), 1)
	pagination := util.VaryCursorBasedPaginationForEvents(limit, cursor)
	paginationMap := map[string]any{"limit": pagination.Limit}
	if pagination.Cursor != nil {
		paginationMap["cursor"] = pagination.Cursor.String()
		delete(entry, "startLedger")
		delete(entry, "endLedger")
	}
	return paginationMap
}
