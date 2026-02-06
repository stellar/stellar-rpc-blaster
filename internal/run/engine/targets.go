package engine

import (
	"net/http"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// Creates a Vegeta Targeter for JSON-RPC requests
// TODO: support headers to demarcate these as load test requests, parameters
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
