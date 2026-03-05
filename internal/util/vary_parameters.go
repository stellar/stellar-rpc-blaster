package util

import (
	"math/rand/v2"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/collections/set"
)

// VaryLimit returns a random limit between 1 and the provided max lVmit.
func VaryLimit(limitMax uint) uint {
	return uint(rand.Float64()*float64(limitMax-1) + 1)
}

// VaryFormat determines whether to use "json" or "base64" format for transaction requests.
// Used by all data-dependent endpoints.
func VaryFormat() string {
	if rand.Float64() < PrJson {
		return "json"
	}
	return "base64"
}

// randomly decides whether to include a pagination cursor in the request, to vary request patterns for data-dependent endpoints that paginate
// used by getEvents, getTransactions, getLedgers
func VaryCursorBasedPagination(limit uint, cursor string) *protocol.LedgerPaginationOptions {
	pagination := &protocol.LedgerPaginationOptions{
		Limit: limit,
	}
	if rand.Float64() < PrCursor {
		pagination.Cursor = cursor
	}
	return pagination
}

// used only by getEvents, which has a different pagination struct
func VaryCursorBasedPaginationForEvents(limit uint, cursor string) *protocol.PaginationOptions {
	pagination := &protocol.PaginationOptions{
		Limit: limit,
	}
	if rand.Float64() < PrCursor {
		c, err := protocol.ParseCursor(cursor)
		if err != nil {
			// if cursor parsing fails, fall back to no cursor rather than erroring out, since this is just for request variation
			return &protocol.PaginationOptions{Limit: limit}
		}
		pagination.Cursor = &c
	}
	return pagination
}

// Chooses key count according to the distribution defined in PrKeyCount
// 80% of requests have 1 key, 15% have between 2 and 10, and 5% will have between 50 and LedgerKeyLimit=200 keys
// Used by getLedgerEntries to vary the number of keys requested.
func VaryKeyCount() uint {
	r := rand.Float64()
	if r < PrKeyCount[0] {
		return 1
	} else if r < PrKeyCount[0]+PrKeyCount[1] {
		return VaryLimit(10 - 1)
	} else {
		return VaryLimit(LedgerKeyLimit-50) + 50 // between 50 and LedgerKeyLimit
	}
}

func VaryEventsFilter(contractIDs []string) map[string]any {
	filter := make(map[string]any)
	if len(contractIDs) > 0 && rand.Float64() < PrEventContractFilter {
		if rand.Float64() < PrEventMultiContract {
			n := 2 + rand.IntN(min(4, len(contractIDs)))
			ids := ChooseNAtRandom(contractIDs, n)
			filter["contractIds"] = ids
		} else {
			cid := contractIDs[rand.IntN(len(contractIDs))]
			filter["contractIds"] = []string{cid}
		}
	}

	return filter
}

func ChooseNAtRandom[T any](items []T, n int) []T {
	if n >= len(items) {
		return items
	}
	chosen := set.NewSet[any](n)
	result := make([]T, 0, n)
	continuedCounter := 0
	for len(chosen) < n {
		item := items[rand.IntN(len(items))]
		if chosen.Contains(item) {
			continuedCounter++
			if continuedCounter > n*10 { // safeguard against tiny sample size
				break
			}
			continue
		}
		chosen.Add(item)
		result = append(result, item)
	}
	return result
}
