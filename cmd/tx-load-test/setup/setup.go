package setup

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// Step is a single phase of the ledger-state setup.
// Implementations are executed in order by Setup.
type Step interface {
	// Name returns a short human-readable description used in log output and
	// error messages.
	Name() string
	// Run executes the step. It may read from and write into st.
	Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error
}

// steps is the ordered list of setup phases.
// OZ-token and Soroswap steps are intentionally omitted until those workloads
// are implemented; only the SAC-transfer workload is active.
var steps = []Step{
	feePayerStep{},
	assetsStep{},
	accountsStep{},
	sacStep{},
}

// Setup orchestrates the full ledger-state setup before benchmarking begins.
// It runs each Step in dependency order and populates a State value that the
// benchmark phase consumes.
//
// If existing is non-nil (loaded from a previous state.json), Setup reuses it
// as the starting point so only the delta work is performed (e.g. creating
// additional accounts to reach cfg.NumberOfAccounts).
func Setup(ctx context.Context, logger *log.Entry, cfg config.Config, existing *state.State) (*state.State, error) {
	st := existing
	if st == nil {
		st = &state.State{}
	}

	for i, step := range steps {
		scoped := logger.WithField("phase", step.Name())
		scoped.Infof("step %d/%d", i+1, len(steps))
		if err := step.Run(ctx, scoped, cfg, st); err != nil {
			scoped.WithError(err).Error("step failed")
			return nil, fmt.Errorf("%s: %w", step.Name(), err)
		}
	}

	logger.Info("all steps complete")
	return st, nil
}
