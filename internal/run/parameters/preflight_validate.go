package parameters

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
)

type endpointSeedValidationClient interface {
	GetHealth(ctx context.Context) (protocol.GetHealthResponse, error)
}

type EndpointSeedValidator struct {
	client   endpointSeedValidationClient
	seedData seed.SeedData
}

func NewEndpointSeedValidator(client endpointSeedValidationClient, seedData seed.SeedData) *EndpointSeedValidator {
	return &EndpointSeedValidator{client: client, seedData: seedData}
}

// ValidateConfiguredEndpoints runs seed freshness probes for the enabled endpoints
// before run mode starts sending load.
func (v *EndpointSeedValidator) ValidateConfiguredEndpoints(ctx context.Context, endpointKeys []string) error {
	if v == nil || v.client == nil {
		return nil
	}

	requiresSeedData := false
	for _, endpointKey := range endpointKeys {
		needsData, err := EndpointNeedsData(endpointKey)
		if err != nil {
			return fmt.Errorf("failed to determine whether endpoint %s needs seed data: %w", endpointKey, err)
		}
		if needsData {
			requiresSeedData = true
			break
		}
	}
	if !requiresSeedData {
		return nil
	}

	return v.validateRetentionWindow(ctx)
}

func (v *EndpointSeedValidator) validateRetentionWindow(ctx context.Context) error {
	oldestSeedLedger := v.seedData.LedgerRange.First

	resp, err := v.client.GetHealth(ctx)
	if err != nil {
		return fmt.Errorf("target RPC health check failed: %w", err)
	}
	if resp.LatestLedger == 0 {
		return fmt.Errorf("target RPC health check did not report a latest ledger")
	}
	if oldestSeedLedger < resp.OldestLedger {
		return fmt.Errorf(
			"seed data start ledger %d is outside the RPC retention window [%d, %d]",
			oldestSeedLedger,
			resp.OldestLedger,
			resp.LatestLedger,
		)
	}
	return nil
}
