package util

import (
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

var TxHashSuccessRatio float64

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

func BuildTxHashReqBody(txHash string) map[string]any {
	return map[string]any{
		"tx_hash": txHash,
		"format":  VaryFormat(),
	}
}

// SetTxHashSuccessRatio sets the ratio of successful vs failed txHashes based on seed data counts.
func SetTxHashSuccessRatio(successes, failures int) {
	total := successes + failures
	if total == 0 {
		TxHashSuccessRatio = 0
	} else {
		TxHashSuccessRatio = float64(successes) / float64(total)
	}
}
