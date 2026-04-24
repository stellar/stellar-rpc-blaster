package parameters

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

type endpointSeedValidationClient interface {
	GetHealth(ctx context.Context) (protocol.GetHealthResponse, error)
}

// ValidateConfiguredEndpoints runs seed freshness probes for the enabled endpoints
// before run mode starts sending load.
func (p *Parameters) ValidateConfiguredEndpoints(ctx context.Context, client endpointSeedValidationClient, endpointKeys []string) error {
	if p == nil || client == nil {
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

	return p.validateRetentionWindow(ctx, client)
}

func (p *Parameters) validateRetentionWindow(ctx context.Context, client endpointSeedValidationClient) error {
	oldestSeedLedger := p.Output.LedgerRange.First

	resp, err := client.GetHealth(ctx)
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
