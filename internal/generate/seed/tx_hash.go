package seed

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// TxHashSeeder collects transaction hashes split by success/failure status.
type TxHashSeeder struct {
	rpcClient *rpcclient.Client
	logger    *log.Entry
	data      []string
}

func NewTxHashSeeder(rpcClient *rpcclient.Client) *TxHashSeeder {
	return &TxHashSeeder{
		rpcClient: rpcClient,
		data:      make([]string, 0, util.DefaultSeedSliceSize),
	}
}

// WriteResults writes the accumulated transaction hashes to the SeedWriter.
func (s *TxHashSeeder) WriteResults(w *SeedWriter) {
	w.TxHashes = s.data
}

// SeedDataForRange implements Seeder for TxHashSeeder by getting hashes and categorizing them by success or failure.
func (s *TxHashSeeder) SeedDataForRange(ctx context.Context, r Range) error {
	var cursor string
	for {
		req := util.MakeGetTransactionsRequest(r.First, cursor)

		txsResponse, err := s.rpcClient.GetTransactions(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to fetch transaction data for ledger %d, cursor %s: %v", r.First, cursor, err)
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

func (s *TxHashSeeder) String() string {
	return "transaction hash data"
}
