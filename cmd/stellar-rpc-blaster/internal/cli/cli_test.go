package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLedgerWindow(t *testing.T) {
	cases := map[string]struct {
		in     string
		window []int64
		err    string
	}{
		"unset":             {"", nil, ""},
		"START":             {"100", []int64{100}, ""},
		"START,END":         {"100,200", []int64{100, 200}, ""},
		"spaces":            {" 100 , 200 ", []int64{100, 200}, ""},
		"too many":          {"1,2,3", nil, "query-window must have at most 2 values, got 3"},
		"not a number":      {"55000000:55100000", nil, `query-window: "55000000:55100000" is not a ledger sequence`},
		"trailing comma":    {"100,", nil, `query-window: "" is not a ledger sequence`},
		"newest N":          {"-120960", []int64{-120960}, ""},
		"newest N with END": {"-120960,55000000", nil, "query-window: -N (the newest N ledgers) takes no END"},
		"negative END":      {"100,-5", nil, `query-window: "-5" is not a ledger sequence`},
		"-0":                {"-0", nil, `query-window: "-0" is not a ledger sequence`},
		"past uint32":       {"4294967296", nil, `query-window: "4294967296" is not a ledger sequence`},
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
