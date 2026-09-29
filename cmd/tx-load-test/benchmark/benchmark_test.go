package benchmark

import (
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
	"github.com/stretchr/testify/require"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name: "valid sac transfer config",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				ClassicRPS:       0,
				NumberOfAccounts: 500,
			},
			wantErr: false,
		},
		{
			name: "valid oz transfer config",
			cfg: config.Config{
				Mode:             config.ModeOZTransfer,
				TargetRPS:        50,
				ClassicRPS:       0,
				NumberOfAccounts: 500,
			},
			wantErr: false,
		},
		{
			name: "valid soroswap config shape",
			cfg: config.Config{
				Mode:             config.ModeSoroswap,
				TargetRPS:        50,
				ClassicRPS:       0,
				NumberOfAccounts: 500,
			},
			wantErr: false,
		},
		{
			name: "rejects unsupported mode",
			cfg: config.Config{
				Mode:             config.BenchmarkMode("unknown-mode"),
				TargetRPS:        50,
				ClassicRPS:       0,
				NumberOfAccounts: 500,
			},
			wantErr: true,
		},
		{
			name: "rejects below hard minimum",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				ClassicRPS:       0,
				NumberOfAccounts: 499,
			},
			wantErr: true,
		},
		{
			name: "rejects below recommended size",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				ClassicRPS:       0,
				NumberOfAccounts: 499,
			},
			wantErr: true,
		},
		{
			name: "accepts exactly recommended minimum",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				ClassicRPS:       0,
				NumberOfAccounts: 500,
			},
			wantErr: false,
		},
		{
			name: "accepts sac transfer when total tx-source pool is large enough",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        250,
				ClassicRPS:       0,
				NumberOfAccounts: 5_000,
			},
			wantErr: false,
		},
		{
			name: "accepts oz transfer with same total account pool",
			cfg: config.Config{
				Mode:             config.ModeOZTransfer,
				TargetRPS:        250,
				ClassicRPS:       0,
				NumberOfAccounts: 5_000,
			},
			wantErr: false,
		},
		{
			name: "rejects dual stream when total account pool too small",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        200,
				ClassicRPS:       200,
				Duration:         2 * time.Minute,
				NumberOfAccounts: 2_019,
			},
			wantErr: true,
		},
		{
			name: "accepts dual stream at derived minimum",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        200,
				ClassicRPS:       200,
				Duration:         2 * time.Minute,
				NumberOfAccounts: 2_020,
			},
			wantErr: false,
		},
		{
			name: "rejects classic rate that cannot map to fixed-size batches",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				ClassicRPS:       150,
				Duration:         2 * time.Minute,
				NumberOfAccounts: 600,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.cfg)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateSetupConfig(t *testing.T) {
	t.Run("accepts shape valid for all modes", func(t *testing.T) {
		err := ValidateSetupConfig(config.Config{
			TargetRPS:        200,
			ClassicRPS:       200,
			Duration:         2 * time.Minute,
			NumberOfAccounts: 2_020,
		})
		require.NoError(t, err)
	})

	t.Run("rejects shape invalid for all-mode setup", func(t *testing.T) {
		err := ValidateSetupConfig(config.Config{
			TargetRPS:        200,
			ClassicRPS:       200,
			Duration:         2 * time.Minute,
			NumberOfAccounts: 2_019,
		})
		require.EqualError(t, err, "benchmark shape is not valid for mode=sac-transfer: account pool too small for configured benchmark: have 2019 accounts but need at least 2020 (2000 Soroban tx sources + 20 simple-payment tx sources)  -- increase --accounts, reduce --target-rps, reduce --classic-rps, or shorten --duration")
	})
}

func TestPartitionSourceAccountsUsesRequiredSorobanSlice(t *testing.T) {
	cfg := config.Config{
		Mode:             config.ModeSoroswap,
		TargetRPS:        1,
		ClassicRPS:       0,
		Duration:         10 * time.Second,
		NumberOfAccounts: 2000,
	}
	st := &state.State{AccountKPs: make([]*keypair.Full, 2000)}

	simpleSources, sorobanSources, err := partitionSourceAccounts(cfg, st)
	require.NoError(t, err)
	require.Len(t, simpleSources, 0)
	require.Len(t, sorobanSources, state.SorobanSourceAccountCount(cfg))
}
