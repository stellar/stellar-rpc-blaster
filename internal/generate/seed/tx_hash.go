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
) error {
	if err := writer.StartArray("tx_hashes"); err != nil {
		return errors.Wrap(err, "failed to start tx_hashes array")
	}

	var cursor string
	for {
		req := protocol.GetTransactionsRequest{
			StartLedger: parameters.Range.First,
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
			return errors.Wrapf(err, "failed to fetch transaction data for ledger %d, cursor %s",
				parameters.Range.First, cursor)
		}
		if len(txsResponse.Transactions) == 0 {
			break
		}
		cursor = txsResponse.Cursor

		for _, tx := range txsResponse.Transactions {
			if tx.Ledger > parameters.Range.Last {
				if err := writer.EndArray(); err != nil {
					return errors.Wrap(err, "failed to end tx_hashes array")
				}
				return nil
			}
			item := TxData{
				TxHash:  tx.TransactionDetails.TransactionHash,
				Success: tx.TransactionDetails.Status == TxSuccess,
			}
			if err := writer.WriteItem(item); err != nil {
				return errors.Wrap(err, "failed to write tx hash item")
			}
		}
	}
	if err := writer.EndArray(); err != nil {
		return errors.Wrap(err, "failed to end tx_hashes array")
	}
	return nil
}
