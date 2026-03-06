package util

import (
	"math/rand/v2"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

// VaryLimit returns a random limit between 1 and the provided max limit.
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

func ChooseNAtRandom[T any](items []T, n int) []T {
	if n >= len(items) {
		return items
	}
	indices := make([]int, len(items))
	for i := range indices {
		indices[i] = i
	}
	for i := range n {
		j := i + rand.IntN(len(indices)-i)
		indices[i], indices[j] = indices[j], indices[i]
	}
	result := make([]T, n)
	for i := range n {
		result[i] = items[indices[i]]
	}
	return result
}

func WeightedChooseN[T any](items []T, weights []int, n int) []T {
	if n >= len(items) {
		return items
	}
	w := make([]int, len(weights))
	copy(w, weights)

	result := make([]T, 0, n)
	for range n {
		total := 0
		for _, wt := range w {
			total += wt
		}
		if total == 0 {
			break
		}
		r := rand.IntN(total)
		cumulative := 0
		for i, wt := range w {
			cumulative += wt
			if r < cumulative {
				result = append(result, items[i])
				w[i] = 0
				break
			}
		}
	}
	return result
}
