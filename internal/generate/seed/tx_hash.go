package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// TxHashSeeder collects transaction hashes split by success/failure status.
type TxHashSeeder struct {
	data []string
}

func NewTxHashSeeder() *TxHashSeeder {
	return &TxHashSeeder{}
}

// Results returns the accumulated transaction hash data.
func (s *TxHashSeeder) Results() []string {
	return s.data
}

// SeedDataForRange implements Seeder for TxHashSeeder by getting hashes and categorizing them by success or failure.
func (s *TxHashSeeder) SeedDataForRange(
	ctx context.Context,
	_ *log.Entry,
	rpcClient *rpcclient.Client,
	r Range,
) error {
	var cursor string
	for {
		req := util.MakeGetTransactionsRequest(r.First, cursor)

		txsResponse, err := rpcClient.GetTransactions(ctx, req)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch transaction data for ledger %d, cursor %s", r.First, cursor)
		}
		if len(txsResponse.Transactions) == 0 {
			break
		}
		cursor = txsResponse.Cursor

		for _, tx := range txsResponse.Transactions {
			if tx.Ledger > r.Last {
				return nil
			}
			hash := tx.TransactionDetails.TransactionHash
			s.data = append(s.data, hash)
		}
	}
	return nil
}
