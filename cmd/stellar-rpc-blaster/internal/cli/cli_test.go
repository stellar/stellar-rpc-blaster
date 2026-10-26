package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLedgerWindow(t *testing.T) {
	cases := map[string]struct {
		in     string
		window []uint32
		err    string
	}{
		"unset":          {"", nil, ""},
		"START":          {"100", []uint32{100}, ""},
		"START,END":      {"100,200", []uint32{100, 200}, ""},
		"spaces":         {" 100 , 200 ", []uint32{100, 200}, ""},
		"too many":       {"1,2,3", nil, "query-window must have at most 2 values, got 3"},
		"not a number":   {"55000000:55100000", nil, `query-window: "55000000:55100000" is not a ledger sequence`},
		"trailing comma": {"100,", nil, `query-window: "" is not a ledger sequence`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			window, err := parseLedgerWindow("query-window", c.in)
			if c.err != "" {
				require.ErrorContains(t, err, c.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.window, window)
		})
	}
}
