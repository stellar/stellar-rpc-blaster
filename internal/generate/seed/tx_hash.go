package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

var (
	TxSuccess string = "SUCCESS"
	TxFailed  string = "FAILED"
)

type TxData struct {
	TxHash  string `json:"txHash"`
	Success bool   `json:"success"`
}

func SeedTxHashData(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	writer *SeedWriter,
	parameters PreseedParameters,
) (uint32, error) {
	var txHashCount uint32
	entry := NewEntry[TxData]("tx_hashes", 64)

	for _, r := range parameters.GetProcessingRanges() {
		if txHashCountForRange, err := seedTxHashesForRange(ctx, rpcClient, &entry, r); err != nil {
			return 0, err
		} else {
			txHashCount += txHashCountForRange
		}
	}
	return txHashCount, writer.FlushMap(entry.Map)
}

func seedTxHashesForRange(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	entry *Entry[TxData],
	r Range,
) (uint32, error) {
	var cursor string
	var txHashCountForRange uint32
	for {
		req := protocol.GetTransactionsRequest{
			StartLedger: r.First,
			Pagination: &protocol.LedgerPaginationOptions{
				Limit: uint(util.TxPageLimit),
			},
		}
		if cursor != "" {
			req.StartLedger = 0
			req.Pagination.Cursor = cursor
		}

		txsResponse, err := rpcClient.GetTransactions(ctx, req)
		if err != nil {
			return 0, errors.Wrapf(err, "failed to fetch transaction data for ledger %d, cursor %s",
				r.First, cursor)
		}
		if len(txsResponse.Transactions) == 0 {
			break
		}
		cursor = txsResponse.Cursor

		txHashCountForRange += uint32(len(txsResponse.Transactions))
		for _, tx := range txsResponse.Transactions {
			if tx.Ledger > r.Last {
				return txHashCountForRange, nil
			}
			item := TxData{
				TxHash:  tx.TransactionDetails.TransactionHash,
				Success: tx.TransactionDetails.Status == TxSuccess,
			}
			entry.Append(item)
		}
	}
	return txHashCountForRange, nil
}
