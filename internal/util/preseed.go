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

func GetLedgerRange(ctx context.Context, rpcClient *rpcclient.Client, ledgerWindow uint32) (uint32, uint32, error) {
	latestCheckpointLedger, err := GetLatestCheckpointLedger(ctx, rpcClient)
	if err != nil {
		return 0, 0, errors.Wrap(err, "failed to get latest checkpoint ledger")
	}

	if ledgerWindow > latestCheckpointLedger {
		return 0, 0, errors.Errorf(
			"ledger window (%d) exceeds latest checkpoint ledger (%d)",
			ledgerWindow, latestCheckpointLedger,
		)
	}

	return latestCheckpointLedger - ledgerWindow, latestCheckpointLedger, nil
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
