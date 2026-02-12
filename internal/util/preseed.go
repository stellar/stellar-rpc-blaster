package util

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	checkpoint "github.com/stellar/go-stellar-sdk/historyarchive"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

func GetLatestCheckpointLedger(ctx context.Context, rpcClient *rpcclient.Client) (uint32, error) {
	checkpointManager := checkpoint.NewCheckpointManager(CheckpointFrequency)
	latestLedger, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "failed to fetch latest ledger")
	}

	return checkpointManager.PrevCheckpoint(latestLedger.Sequence), nil
}

func GetLedgerRange(ctx context.Context, rpcClient *rpcclient.Client, ledgerWindow []uint32, count uint32) (uint32, uint32, error) {
	latestCheckpointLedger, err := GetLatestCheckpointLedger(ctx, rpcClient)
	if err != nil {
		return 0, 0, errors.Wrap(err, "failed to get latest checkpoint ledger")
	}

	var first, last uint32
	switch len(ledgerWindow) {
	case 0:
		// No window specified: [LatestCheckpoint - count, LatestCheckpoint]
		if count > latestCheckpointLedger {
			return 0, 0, errors.Errorf("count (%d) exceeds latest checkpoint ledger (%d)", count, latestCheckpointLedger)
		}
		first = latestCheckpointLedger - count + 1
		last = latestCheckpointLedger
	case 1:
		// Only START: [START, LatestCheckpoint]
		first = ledgerWindow[0]
		last = latestCheckpointLedger
	case 2:
		// Both START and END: [START, END]
		first = ledgerWindow[0]
		last = ledgerWindow[1]
	default:
		return 0, 0, errors.Errorf("ledger-window must have at most 2 values, got %d", len(ledgerWindow))
	}

	if first > last {
		return 0, 0, errors.Errorf("invalid ledger range: start (%d) > end (%d)", first, last)
	}

	return first, last, nil
}

// Returns count uniformly-spaced ledger sequence numbers
// in [first, last]
func ComputeSampledLedgers(first, last, count uint32) []uint32 {
	windowSize := last - first + 1
	if count >= windowSize {
		return nil // range already has <= count ledgers, no need to sample
	}

	sampled := make([]uint32, count)
	step := float64(last-first) / float64(count-1)
	for i := range count {
		sampled[i] = first + uint32(float64(i)*step+0.5)
	}
	return sampled
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
