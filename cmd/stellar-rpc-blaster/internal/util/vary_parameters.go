package util

import (
	"math/rand/v2"
	"slices"
)

// VaryLimit returns a random limit between 1 and the provided max limit.
func VaryLimit(limitMax uint, rng *rand.Rand) uint {
	return uint(rng.Float64()*float64(limitMax-1) + 1)
}

// VaryFormat determines whether to use "json" or "base64" format.
// Used by getLedgerEntries; the modeled endpoints omit the key (base64 default).
func VaryFormat(rng *rand.Rand) string {
	if rng.Float64() < PrJson {
		return "json"
	}
	return "base64"
}

// Chooses key count according to the distribution defined in PrKeyCount
// 80% of requests have 1 key, 15% have between 2 and 10, and 5% will have between 50 and LedgerKeyLimit=200 keys
// Used by getLedgerEntries to vary the number of keys requested.
func VaryKeyCount(rng *rand.Rand) uint {
	r := rng.Float64()
	if r < PrKeyCount[0] {
		return 1
	} else if r < PrKeyCount[0]+PrKeyCount[1] {
		return VaryLimit(10-1, rng)
	} else {
		return VaryLimit(LedgerKeyLimit-50, rng) + 50 // between 50 and LedgerKeyLimit
	}
}

// ChooseNAtRandomSeeded chooses N from items without replacement using the Fisher-Yates
// shuffle, drawing from a caller-provided rand source so runs stay reproducible.
func ChooseNAtRandomSeeded[T any](items []T, n int, rng *rand.Rand) []T {
	if n >= len(items) {
		return items
	}
	indices := make([]int, len(items))
	for i := range indices {
		indices[i] = i
	}
	for i := range n {
		j := i + rng.IntN(len(indices)-i)
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
