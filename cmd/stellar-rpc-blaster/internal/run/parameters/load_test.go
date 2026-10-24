package parameters

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
		"too many values":              {[]uint32{1, 2, 3}, 0, 0, "query-window must have at most 2 values, got 3"},
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
		})
	}
}
