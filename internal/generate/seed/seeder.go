package seed

import (
	"context"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// Seeder processes seed data for a specific RPC data type across ledger ranges.
// Each implementation holds its own accumulator state and exposes result getters on the struct.
type Seeder interface {
	// SeedDataForRange collects data from a single ledger range.
	SeedDataForRange(
		ctx context.Context,
		logger *log.Entry,
		rpcClient *rpcclient.Client,
		r util.Range,
	) error
}

// RunSeeder iterates over processing ranges and delegates collection to the given Seeder for each range.
func RunSeeder(
	ctx context.Context,
	logger *log.Entry,
	rpcClient *rpcclient.Client,
	parameters util.PreseedParameters,
	seeder Seeder,
) error {
	for _, r := range parameters.GetProcessingRanges() {
		if err := seeder.SeedDataForRange(ctx, logger, rpcClient, r); err != nil {
			return err
		}
	}
	return nil
}
