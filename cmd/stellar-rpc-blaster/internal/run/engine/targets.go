package engine

import (
	"net/http"
	"sync/atomic"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// NewJSONRPCTargeter creates a Vegeta Targeter that cycles through a set of
// pre-encoded JSON-RPC request bodies so each hit sends different parameters.
// Labeled bodies get their label as a URL query param — the RPC ignores it, but
// vegeta echoes the URL on each Result, letting results be attributed per label.
func NewJSONRPCTargeter(rpcURL string, bodies [][]byte, labels []string) vegeta.Targeter {
	urls := make([]string, len(bodies))
	for i := range urls {
		urls[i] = rpcURL
		if labels != nil {
			urls[i] += "?label=" + labels[i]
		}
	}
	var idx atomic.Uint64 // atomically incr index to rotate through sample bodies
	return func(t *vegeta.Target) error {
		i := (idx.Add(1) - 1) % uint64(len(bodies))
		t.Method = http.MethodPost
		t.URL = urls[i]
		t.Body = bodies[i]
		t.Header = http.Header{
			"Content-Type": []string{"application/json"},
		}
		return nil
	}
}
