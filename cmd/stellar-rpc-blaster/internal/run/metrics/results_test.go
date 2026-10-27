package blasterMetrics

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/run/parameters"
)

func TestResultsQueryWindow(t *testing.T) {
	a := &Aggregator{stats: map[string]*EndpointStats{}}
	data, err := a.Results().MarshalJSON()
	require.NoError(t, err)
	require.NotContains(t, string(data), "query_window", "no seed data, no window")

	a.head = parameters.HeadInfo{Oldest: 100, Latest: 50_000, Pinned: true}
	require.Equal(t, &QueryWindow{First: 1100, Last: 50_000, Oldest: 100, Pinned: true}, a.Results().Window)
	data, err = a.Results().MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(data), `"query_window": {`)
}
