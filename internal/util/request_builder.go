package util

import (
	"encoding/json"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

func MarshalJsonRpcRequest(method string, params map[string]any) ([]byte, error) {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	return json.Marshal(req)
}

func MakeGetTransactionsRequest(start uint32, cursor string) protocol.GetTransactionsRequest {
	req := protocol.GetTransactionsRequest{
		StartLedger: start,
		Pagination: &protocol.LedgerPaginationOptions{
			Limit: uint(TxPageLimit),
		},
	}
	if cursor != "" {
		req.StartLedger = 0
		req.Pagination.Cursor = cursor
	}
	return req
}

func MakeGetEventsRequest(start uint32, end uint32, cursor *protocol.Cursor) protocol.GetEventsRequest {
	req := protocol.GetEventsRequest{
		StartLedger: start,
		EndLedger:   end,
		Filters: []protocol.EventFilter{
			{
				EventType: protocol.EventTypeSet{
					protocol.EventTypeContract: nil, // only key presence matters
				},
			},
		},
		Pagination: &protocol.PaginationOptions{
			Cursor: cursor,
		},
	}
	// When paginating with a cursor, ledger fields must be unset.
	if cursor != nil {
		req.StartLedger = 0
		req.EndLedger = 0
	}
	return req
}

func MakeSendTransactionRequest(txB64 string) protocol.SendTransactionRequest {
	return protocol.SendTransactionRequest{
		Transaction: txB64,
	}
}
