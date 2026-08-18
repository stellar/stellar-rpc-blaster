package parameters

import (
	"context"
	"fmt"
	"strings"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

type endpointSeedValidationClient interface {
	GetHealth(ctx context.Context) (protocol.GetHealthResponse, error)
}

// ValidateConfiguredEndpoints checks the seed has the fields the enabled endpoints
// draw from and runs seed freshness probes, before run mode starts sending load.
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
		requiresSeedData = requiresSeedData || needsData
	}
	if !requiresSeedData {
		return nil
	}
	if missing := p.missingSeedFields(endpointKeys); len(missing) > 0 {
		return fmt.Errorf("seed data is missing %s, needed by the configured endpoints — rerun generate", strings.Join(missing, ", "))
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
	p.Head = HeadInfo{Oldest: resp.OldestLedger, Latest: resp.LatestLedger}
	return nil
}
