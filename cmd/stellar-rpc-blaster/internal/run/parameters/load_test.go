package parameters

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHeadMargin(t *testing.T) {
	h := HeadInfo{Oldest: 100, Latest: 50_000}
	require.EqualValues(t, 1100, h.Floor(), "at least the left-edge margin")
	require.EqualValues(t, 180, DriftLedgers(10*time.Minute), "ledgers closing in 10m, plus half again")
	h.Drift = DriftLedgers(2 * time.Hour)
	require.EqualValues(t, 2160, h.Drift)
	require.EqualValues(t, 2260, h.Floor(), "a long run widens the margin")
}

func TestHeadNarrow(t *testing.T) {
	cases := map[string]struct {
		window        []uint32
		floor, latest uint32
		err           string
	}{
		"unset: [oldest+1000, latest]": {nil, 1100, 50_000, ""},
		"START only":                   {[]uint32{20_000}, 20_000, 50_000, ""},
		"START,END":                    {[]uint32{20_000, 30_000}, 20_000, 30_000, ""},
		"clamped at both ends":         {[]uint32{500, 60_000}, 1100, 50_000, ""},
		"entirely below the floor":     {[]uint32{200, 900}, 0, 0, "query-window [200, 900] has no ledgers in the target's queryable range [1100, 50000]"},
		"START past latest":            {[]uint32{60_000}, 0, 0, "query-window [60000, 50000] has no ledgers in the target's queryable range [1100, 50000]"},
		"START after END":              {[]uint32{30_000, 20_000}, 0, 0, "query-window [30000, 20000] has no ledgers in the target's queryable range [1100, 50000]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := HeadInfo{Oldest: 100, Latest: 50_000}
			err := h.Narrow(c.window)
			if c.err != "" {
				require.ErrorContains(t, err, c.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, [2]uint32{c.floor, c.latest}, [2]uint32{h.Floor(), h.Latest})
			require.Equal(t, len(c.window) == 2, h.Pinned, "an END pins the head")
		})
	}
}
