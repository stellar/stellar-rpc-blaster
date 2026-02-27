package seed

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestComputeSampledLedgers verifies that, given a window and count where the size of the window exceeds the
// count, the generated ledger ranges are uniformly distributed across the window.
func TestComputeSampledLedgers(t *testing.T) {
	window := Range{
		First: 10,
		Last:  20,
	}
	counts := []uint32{2, 5, 9}
	for _, count := range counts {
		sampled, err := ComputeSampledLedgers(window, count)
		assert.NoError(t, err, "ComputeSampledLedgers failed unexpectedly")
		assert.Equal(t, count, len(sampled), "expected count=%d sampled ledgers, got %d", count, len(sampled))

		var minGap, maxGap uint32 = math.MaxUint32, 0
		for i := 1; i < len(sampled); i++ {
			g := sampled[i] - sampled[i-1]
			minGap = min(g, minGap)
			maxGap = max(g, maxGap)
		}
		assert.LessOrEqual(t, maxGap-minGap, 1, "non-uniform gaps: min=%d max=%d in %v", minGap, maxGap, sampled)
	}
}
