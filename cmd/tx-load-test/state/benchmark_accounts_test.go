package state

import (
	"testing"
	"time"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stretchr/testify/require"
)

func TestAccountSizingFormula(t *testing.T) {
	cfg := config.Config{
		Mode:       config.ModeSACTransfer,
		TargetRPS:  200,
		ClassicRPS: 200,
		Duration:   2 * time.Minute,
	}

	require.Equal(t, 2000, SorobanSourceAccountCount(cfg))
	require.Equal(t, 2, SimplePaymentTransactionRate(cfg))
	require.Equal(t, 20, SimplePaymentSourceAccountCount(cfg))
	require.Equal(t, 2, HolderAccountCount(cfg))
}
