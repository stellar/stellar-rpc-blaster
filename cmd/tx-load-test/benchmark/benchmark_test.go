package benchmark

import (
	"testing"
	"time"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
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
