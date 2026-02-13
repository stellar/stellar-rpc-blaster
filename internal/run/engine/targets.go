package engine

import (
	"net/http"
	"sync/atomic"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// Creates a Vegeta Targeter for JSON-RPC requests, used for endpoints that don't require parameterization
func NewJSONRPCTargeter(rpcURL string, body []byte) vegeta.Targeter {
	return func(t *vegeta.Target) error {
		t.Method = http.MethodPost
		t.URL = rpcURL
		t.Body = body
		t.Header = http.Header{
			"Content-Type": []string{"application/json"},
		}
		return nil
	}
}

// NewRotatingJSONRPCTargeter creates a Vegeta Targeter that cycles through
// a set of pre-encoded JSON-RPC request bodies so each hit sends different parameters
func NewRotatingJSONRPCTargeter(rpcURL string, bodies [][]byte) vegeta.Targeter {
	var idx atomic.Uint64 // atomically incr index to rotate through sample bodies
	return func(t *vegeta.Target) error {
		i := idx.Add(1) - 1
		t.Method = http.MethodPost
		t.URL = rpcURL
		t.Body = bodies[i%uint64(len(bodies))]
		t.Header = http.Header{
			"Content-Type": []string{"application/json"},
		}
		return nil
	}
}
