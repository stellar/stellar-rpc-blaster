package seed

import (
	"context"
)

// Seeder processes seed data for a specific RPC data type across ledger ranges.
// Each implementation holds its own accumulator state and exposes result getters on the struct.
type Seeder interface {
	// SeedDataForRange collects data from a single ledger range.
	SeedDataForRange(
		ctx context.Context,
		r Range,
	) error
}

// RunSeeder iterates over processing ranges and delegates collection to the given Seeder for each range.
func RunSeeder(
	ctx context.Context,
	parameters PreseedParameters,
	seeder Seeder,
) error {
	for _, r := range parameters.GetProcessingRanges() {
		if err := seeder.SeedDataForRange(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
