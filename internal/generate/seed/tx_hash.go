package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

var (
	TxSuccess string = "SUCCESS"
	TxFailed  string = "FAILED"
)

const TxLimit uint32 = 200

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
	if err := writer.StartArray("txs"); err != nil {
		return errors.Wrap(err, "failed to start txs array")
	}

	limit := min(TxLimit, parameters.Range.Last-parameters.Range.First+1)

	for ; parameters.Range.First < parameters.Range.Last; parameters.Range.First += TxLimit {
		req := protocol.GetTransactionsRequest{
			StartLedger: parameters.Range.First,
			Pagination:  &protocol.LedgerPaginationOptions{Limit: uint(limit)},
		}
		txsResponse, err := rpcClient.GetTransactions(ctx, req)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch transaction data for ledgers %d->%d",
				parameters.Range.First, parameters.Range.First+limit-1)
		}
		for _, tx := range txsResponse.Transactions {
			item := TxData{
				TxHash:  tx.TransactionDetails.TransactionHash,
				Success: tx.TransactionDetails.Status == TxSuccess,
			}
			if err := writer.WriteItem(item); err != nil {
				return errors.Wrap(err, "failed to write tx item")
			}
		}
	}

	if err := writer.EndArray(); err != nil {
		return errors.Wrap(err, "failed to end txs array")
	}
	return nil
}
