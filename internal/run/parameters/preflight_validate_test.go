package parameters

import (
	"context"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type preflightEndpointSeedValidationClient struct {
	healthErr      error
	healthResponse protocol.GetHealthResponse
}

func (s *preflightEndpointSeedValidationClient) GetHealth(
	_ context.Context,
) (protocol.GetHealthResponse, error) {
	return s.healthResponse, s.healthErr
}

func TestValidatorChecksSeedStartForSeededEndpoints(t *testing.T) {
	client := &preflightEndpointSeedValidationClient{
		healthResponse: protocol.GetHealthResponse{
			LatestLedger: 500,
			OldestLedger: 100,
		},
	}
	params := seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 456}}

	err := NewEndpointSeedValidator(client, params).ValidateConfiguredEndpoints(
		context.Background(),
		[]string{"getEvents"},
	)
	require.NoError(t, err)
}

func TestValidatorSkipsStaticEndpoints(t *testing.T) {
	client := &preflightEndpointSeedValidationClient{}
	params := seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 456}}

	err := NewEndpointSeedValidator(client, params).ValidateConfiguredEndpoints(
		context.Background(),
		[]string{"getHealth", "getNetwork"},
	)
	require.NoError(t, err)
}

func TestValidatorRejectsStaleStart(t *testing.T) {
	client := &preflightEndpointSeedValidationClient{
		healthResponse: protocol.GetHealthResponse{
			LatestLedger: 400,
			OldestLedger: 300,
		},
	}
	params := seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 350}}

	err := NewEndpointSeedValidator(client, params).ValidateConfiguredEndpoints(
		context.Background(),
		[]string{"getEvents"},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "seed data start ledger 123 is outside the RPC retention window [300, 400]")
}

func TestValidatorRejectsMissingLatestLedger(t *testing.T) {
	client := &preflightEndpointSeedValidationClient{
		healthResponse: protocol.GetHealthResponse{
			OldestLedger: 100,
		},
	}
	params := seed.SeedData{LedgerRange: seed.Range{First: 123, Last: 456}}

	err := NewEndpointSeedValidator(client, params).ValidateConfiguredEndpoints(
		context.Background(),
		[]string{"getLedgerEntries"},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "target RPC health check did not report a latest ledger")
}
