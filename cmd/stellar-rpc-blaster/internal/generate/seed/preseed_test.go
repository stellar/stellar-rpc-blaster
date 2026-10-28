package seed

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

func TestGetLedgerRange(t *testing.T) {
	client := util.NewMockRPCClient(t, func(string, json.RawMessage) any { return map[string]any{"sequence": 10_000} })
	ckpt, err := GetLatestCheckpointLedger(t.Context(), client)
	require.NoError(t, err)
	cases := map[string]struct {
		window      []int64
		first, last uint32
		err         bool
	}{
		"unset: the newest count": {nil, ckpt - 499, ckpt, false},
		"START":                   {[]int64{5000}, 5000, ckpt, false},
		"START,END":               {[]int64{5000, 6000}, 5000, 6000, false},
		"-N: the newest N":        {[]int64{-100}, ckpt - 99, ckpt, false},
		"-N before ledger 1":      {[]int64{-100_000}, 0, 0, true},
		"START after END":         {[]int64{6000, 5000}, 0, 0, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := GetLedgerRange(t.Context(), client, c.window, 500)
			if c.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, Range{First: c.first, Last: c.last}, r)
		})
	}
}

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

func TestGroupSampledLedgersIntoRanges(t *testing.T) {
	ledgers := []uint32{10, 11, 12, 14, 15, 9, 17}
	ranges := GroupSampledLedgersIntoRanges(ledgers)
	expected := []Range{
		{First: 9, Last: 12},
		{First: 14, Last: 15},
		{First: 17, Last: 17},
	}
	assert.Equal(t, expected, ranges, "grouping of sampled ledgers into ranges is incorrect")
}
