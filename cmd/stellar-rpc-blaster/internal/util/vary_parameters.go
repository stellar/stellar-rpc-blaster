package util

import (
	"math/rand/v2"
	"slices"
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

// This chooses N at random from T without replacement using the Fisher-Yates shuffle algorithm.
func ChooseNAtRandom[T any](items []T, n int) []T {
	return chooseNAtRandom(items, n, rand.IntN)
}

// ChooseNAtRandomSeeded is like ChooseNAtRandom but uses a caller-provided rand source for deterministic testing.
func ChooseNAtRandomSeeded[T any](items []T, n int, rng *rand.Rand) []T {
	return chooseNAtRandom(items, n, rng.IntN)
}

func chooseNAtRandom[T any](items []T, n int, intN func(int) int) []T {
	if n >= len(items) {
		return items
	}
	indices := make([]int, len(items))
	for i := range indices {
		indices[i] = i
	}
	for i := range n {
		j := i + intN(len(indices)-i)
		indices[i], indices[j] = indices[j], indices[i]
	}
	result := make([]T, n)
	for i := range n {
		result[i] = items[indices[i]]
	}
	return result
}

// WeightedChooseNSeeded chooses N items from T without replacement, each drawn with
// probability weight[i]/sum(remaining weights), using the caller-provided rand source.
func WeightedChooseNSeeded[T any](items []T, weights []float64, n int, rng *rand.Rand) []T {
	if n >= len(items) {
		return items
	}
	w := slices.Clone(weights)
	total := 0.0
	for _, wt := range w {
		total += wt
	}

	result := make([]T, 0, n)
	for range n {
		if total <= 0 {
			break
		}
		r := rng.Float64() * total
		chosen := -1
		for i, wt := range w {
			if wt == 0 {
				continue
			}
			chosen = i // rounding can leave r a hair positive; falls back to the last weighted item
			if r -= wt; r < 0 {
				break
			}
		}
		if chosen < 0 {
			break
		}
		result = append(result, items[chosen])
		total -= w[chosen]
		w[chosen] = 0
	}
	return result
}
