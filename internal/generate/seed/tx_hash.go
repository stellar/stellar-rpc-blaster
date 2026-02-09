package seed

import (
	"context"
	"encoding/json"
	"os"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/protocols/rpc"
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

type Transactions struct {
	Transactions []TxData `json:"txs"`
}

func SeedTxHashData(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	parameters PreseedParameters,
) error {
	transactions := []TxData{}
	limit := min(TxLimit, parameters.MaxLedger-parameters.MinLedger+1)

	for ; parameters.MinLedger < parameters.MaxLedger; parameters.MinLedger += TxLimit {
		req := protocol.GetTransactionsRequest{
			StartLedger: parameters.MinLedger,
			Pagination:  &protocol.LedgerPaginationOptions{Limit: uint(limit)},
		}
		txsResponse, err := rpcClient.GetTransactions(ctx, req)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch transaction data for ledgers %d->%d",
				parameters.MinLedger, parameters.MinLedger+limit-1)
		}
		for _, tx := range txsResponse.Transactions {
			transactions = append(transactions, TxData{
				TxHash:  tx.TransactionDetails.TransactionHash,
				Success: tx.TransactionDetails.Status == TxSuccess,
			})
		}
	}
	if err := flushTransactions(parameters.ExportPath, transactions); err != nil {
		return errors.Wrapf(err, "failed to flush transactions to %s", parameters.ExportPath)
	}
	return nil
}

func flushTransactions(outPath string, transactions []TxData) error {
	data, err := json.Marshal(transactions)
	if err != nil {
		return errors.Wrap(err, "failed to marshal transaction data")
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return errors.Wrapf(err, "failed to write results to %s", outPath)
	}
	return nil
}
