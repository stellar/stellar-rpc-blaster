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

// TxHashData holds transaction hashes split by outcome.
type TxHashData struct {
	Successes []string `json:"successes"`
	Failures  []string `json:"failures"`
}

func SeedTxHashData(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	parameters util.PreseedParameters,
) (TxHashData, error) {
	var result TxHashData

	for _, r := range parameters.GetProcessingRanges() {
		if _, err := seedTxHashesForRange(ctx, rpcClient, &result, r); err != nil {
			return TxHashData{}, err
		}
	}
	return result, nil
}

func seedTxHashesForRange(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	result *TxHashData,
	r util.Range,
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
			hash := tx.TransactionDetails.TransactionHash
			if tx.TransactionDetails.Status == TxSuccess {
				result.Successes = append(result.Successes, hash)
			} else {
				result.Failures = append(result.Failures, hash)
			}
		}
	}
	return txHashCountForRange, nil
}
