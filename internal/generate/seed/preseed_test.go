package seed

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestComputeSampledLedgers verifies that, given a window and count where the size of the window exceeds the
// count, the generated ledger ranges are uniformly distributed across the window.
func TestComputeSampledLedgersValid(t *testing.T) {
	window := Range{
		First: 10,
		Last:  20,
	}
	counts := []uint32{2, 5, 9}
	for _, count := range counts {
		sampled, err := ComputeSampledLedgers(window, count)
		actualCount := uint32(len(sampled))
		assert.NoError(t, err, "ComputeSampledLedgers failed unexpectedly")
		// Length check
		assert.Equal(t, count, actualCount, "expected count=%d sampled ledgers, got %d", count, actualCount)

		// Endpoint check
		assert.Equal(t, window.First, sampled[0],
			"first sampled value should be first window value %d, got %d", window.First, sampled[0])
		assert.Equal(t, window.Last, sampled[actualCount-1],
			"last sampled value should be last window value %d, got %d", window.First, sampled[actualCount-1])

		// Gap uniformity check
		var minGap, maxGap uint32 = math.MaxUint32, 0
		for i := 1; i < len(sampled); i++ {
			g := sampled[i] - sampled[i-1]
			minGap = min(g, minGap)
			maxGap = max(g, maxGap)
		}
		assert.LessOrEqual(t, maxGap-minGap, uint32(1),
			"non-uniform gaps: min=%d max=%d in %v", minGap, maxGap, sampled)
	}
}

func TestComputeSampledLedgersInvalid(t *testing.T) {
	window := Range{
		First: 10,
		Last:  20,
	}
	var tooBig uint32 = 11
	_, err := ComputeSampledLedgers(window, tooBig)
	assert.Error(t, err, "should error if count > length of ledger window")

	var tooSmall uint32 = 1
	_, err = ComputeSampledLedgers(window, tooSmall)
	assert.Error(t, err, "should error if sample size provided is less than 2")
}
