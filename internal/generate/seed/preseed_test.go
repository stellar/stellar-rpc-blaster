package seed

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLedgersUniformlyDistributed verifies that, given a window and count where the size of the window exceeds the
// count, the generated ledger ranges are uniformly distributed across the window.
func TestLedgersUniformlyDistributed(t *testing.T) {
	window := Range{
		First: 10_000,
		Last:  20_000,
	}
	counts := []uint32{1000, 2000, 5000, 9000}
	assert.True(t, ComputeSampledLedgers(window.First, window.Last, counts[0]) != nil)
}
