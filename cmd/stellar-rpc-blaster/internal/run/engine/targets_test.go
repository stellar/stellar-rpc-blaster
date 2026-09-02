package engine

import (
	"testing"

	"github.com/stretchr/testify/require"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// Labels ride as URL fragments: attributable from the Result's echoed URL, but
// never part of the request the RPC receives.
func TestJSONRPCTargeterLabels(t *testing.T) {
	bodies := [][]byte{[]byte(`{"a":1}`), []byte(`{"b":2}`)}
	targeter := NewJSONRPCTargeter("http://rpc:8000/", bodies, []string{"head-poll", "deep-pager"})
	for i, want := range []string{"head-poll", "deep-pager", "head-poll"} { // wraps around
		var tgt vegeta.Target
		require.NoError(t, targeter(&tgt))
		require.Equal(t, "http://rpc:8000/#"+want, tgt.URL)
		require.Equal(t, bodies[i%2], tgt.Body)
		req, err := tgt.Request()
		require.NoError(t, err)
		require.Equal(t, want, req.URL.Fragment)
		require.Equal(t, "/", req.URL.RequestURI(), "fragment must not reach the wire")
	}
	var tgt vegeta.Target
	require.NoError(t, NewJSONRPCTargeter("http://rpc:8000/", bodies, nil)(&tgt))
	require.Equal(t, "http://rpc:8000/", tgt.URL)
}
