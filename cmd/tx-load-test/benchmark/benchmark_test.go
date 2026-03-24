package benchmark

import (
	"testing"

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
				NumberOfAccounts: 500,
			},
			wantErr: false,
		},
		{
			name: "valid oz transfer config",
			cfg: config.Config{
				Mode:             config.ModeOZTransfer,
				TargetRPS:        50,
				NumberOfAccounts: 500,
			},
			wantErr: false,
		},
		{
			name: "rejects unsupported mode",
			cfg: config.Config{
				Mode:             config.BenchmarkMode("unknown-mode"),
				TargetRPS:        50,
				NumberOfAccounts: 500,
			},
			wantErr: true,
		},
		{
			name: "rejects below hard minimum",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				NumberOfAccounts: 249,
			},
			wantErr: true,
		},
		{
			name: "accepts exactly hard minimum",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				NumberOfAccounts: 250,
			},
			wantErr: false,
		},
		{
			name: "accepts below recommended but above hard minimum",
			cfg: config.Config{
				Mode:             config.ModeSACTransfer,
				TargetRPS:        50,
				NumberOfAccounts: 300,
			},
			wantErr: false,
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
