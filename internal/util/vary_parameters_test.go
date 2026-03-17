package util

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newSeededRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, 0))
}

func TestChoosesCorrectCounts(t *testing.T) {
	items := []string{"a", "b", "c"}
	// Returns all when N greater or equal
	result := ChooseNAtRandomSeeded(items, 5, newSeededRand(1))
	assert.Equal(t, items, result)

	// Returns correct count
	result = ChooseNAtRandomSeeded(items, 3, newSeededRand(1))
	assert.Equal(t, items, result)
	assert.Len(t, result, 3)

	// Weighted returns all when N greater or equal
	weights := []int{10, 20, 30, 40}
	result = WeightedChooseNSeeded(items, weights, 5, newSeededRand(1))
	assert.Equal(t, items, result)

	// Weighted returns correct count
	result = WeightedChooseNSeeded(items, weights, 3, newSeededRand(42))
	assert.Len(t, result, 3)
}

func TestChooseNAtRandomDeterministicWithFixedSeed(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	first := ChooseNAtRandomSeeded(items, 3, newSeededRand(42))
	second := ChooseNAtRandomSeeded(items, 3, newSeededRand(42))
	assert.Equal(t, first, second)

	// Weighted version is also deterministic
	weights := []int{1, 2, 3, 4, 5}
	first = WeightedChooseNSeeded(items, weights, 3, newSeededRand(42))
	second = WeightedChooseNSeeded(items, weights, 3, newSeededRand(42))
	assert.Equal(t, first, second)
}

func TestDistributionCoversAllItems(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	counts := make(map[string]int)
	rng := newSeededRand(7)
	runs := 1000
	for range runs {
		result := ChooseNAtRandomSeeded(items, 2, rng)
		for _, v := range result {
			counts[v]++
		}
	}
	// Every item should be chosen at least once across 1000 runs of picking 2 from 5
	for _, item := range items {
		assert.Greater(t, counts[item], 0, "item %q was never chosen", item)
	}
}

func TestWeightedChooseNZeroWeightNeverChosen(t *testing.T) {
	items := []string{"a", "b", "c"}
	weights := []int{50, 50, 0}
	rng := newSeededRand(42)
	for range 500 {
		result := WeightedChooseNSeeded(items, weights, 2, rng)
		for _, v := range result {
			assert.NotEqual(t, "c", v, "zero-weight item should never be chosen")
		}
	}
}

func TestWeightedChooseN_RespectsWeightDistribution(t *testing.T) {
	items := []string{"heavy", "medium", "light"}
	weights := []int{100, 10, 1}
	counts := make(map[string]int)
	rng := newSeededRand(7)
	runs := 1000
	for range runs {
		result := WeightedChooseNSeeded(items, weights, 1, rng)
		counts[result[0]]++
	}
	// "heavy" (weight 100) should be picked far more than "light" (weight 1)
	assert.Greater(t, counts["heavy"], counts["light"],
		"heavy (w=100) should be chosen more often than light (w=1), got heavy=%d light=%d",
		counts["heavy"], counts["light"])
	assert.Greater(t, counts["heavy"], counts["medium"],
		"heavy (w=100) should be chosen more often than medium (w=10), got heavy=%d medium=%d",
		counts["heavy"], counts["medium"])
}

func TestWeightedChooseN_StopsEarlyWhenAllWeightsExhausted(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	weights := []int{1, 0, 0, 0}
	result := WeightedChooseNSeeded(items, weights, 3, newSeededRand(42))
	// Only "a" has nonzero weight, so we can only pick 1 item
	assert.Len(t, result, 1)
	assert.Equal(t, "a", result[0])
}
