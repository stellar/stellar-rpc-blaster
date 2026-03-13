package engine

import (
	"net/http"
	"sync/atomic"

	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters/tx"
	"github.com/stellar/stellar-rpc-blaster/internal/util"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// NewJSONRPCTargeter creates a Vegeta Targeter that cycles through
// a set of pre-encoded JSON-RPC request bodies so each hit sends different parameters
func NewJSONRPCTargeter(rpcURL string, bodies [][]byte) vegeta.Targeter {
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

func NewSendTxTargeter(rpcURL string, pool *tx.AccountPool, networkPassphrase string) vegeta.Targeter {
	return func(t *vegeta.Target) error {
		acct, seq := pool.Next() // round-robin pick an account and get its current sequence number
		innerTx, err := tx.BuildSelfSendTx(acct, seq, networkPassphrase)
		if err != nil {
			return err
		}
		txB64, err := pool.WrapWithOriginFeeBumpB64(innerTx)
		if err != nil {
			return err
		}
		body, err := util.MarshalJsonRpcRequest("sendTransaction", map[string]any{"transaction": txB64})
		if err != nil {
			return err
		}
		t.Method = http.MethodPost
		t.URL = rpcURL
		t.Body = body
		t.Header = http.Header{
			"Content-Type": []string{"application/json"},
		}
		return nil
	}
}
