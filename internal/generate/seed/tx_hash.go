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

	limit := min(util.TxPageLimit, parameters.Range.Last-parameters.Range.First+1)

	for ; parameters.Range.First < parameters.Range.Last; parameters.Range.First += limit {
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
				return errors.Wrap(err, "failed to write tx hash item")
			}
		}
	}

	if err := writer.EndArray(); err != nil {
		return errors.Wrap(err, "failed to end tx_hashes array")
	}
	return nil
}
