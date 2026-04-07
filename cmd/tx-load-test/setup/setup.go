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

func setupSteps(cfg config.Config) []Step {
	steps := []Step{
		feePayerStep{},
		assetsStep{},
		accountsStep{},
		sacStep{},
	}
	if cfg.Mode == config.ModeSoroswap { // before OZ to fail-fast
		steps = append(steps, soroswapPoolsStep{})
		steps = append(steps, liquidityStep{})
	}
	return append(steps, ozTokenStep{})
}

// Setup orchestrates the full ledger-state setup before benchmarking begins.
// It runs each Step in dependency order and populates a State value that the
// benchmark phase consumes.
//
// If existing is non-nil (loaded from a previous state.json), Setup reuses it
// as the starting point so only the delta work is performed (e.g. creating
// additional accounts to reach cfg.NumberOfAccounts).
func Setup(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	existing *state.State,
	persist func(*state.State) error,
) (*state.State, error) {
	st := existing
	if st == nil {
		st = &state.State{}
	}

	steps := setupSteps(cfg)
	for i, step := range steps {
		scoped := logger.WithField("phase", step.Name())
		scoped.Infof("step %d/%d", i+1, len(steps))
		if err := step.Run(ctx, scoped, cfg, st); err != nil {
			if persist != nil {
				if saveErr := persist(st); saveErr != nil {
					scoped.WithError(saveErr).Error("failed to save partial state after step error")
					return st, fmt.Errorf("%s: %w (also failed to save partial state: %v)", step.Name(), err, saveErr)
				}
				scoped.Warn("partial state saved after step error")
			}
			scoped.WithError(err).Error("step failed")
			return st, fmt.Errorf("%s: %w", step.Name(), err)
		}
		if persist != nil {
			if err := persist(st); err != nil {
				scoped.WithError(err).Error("failed to save state after step")
				return st, fmt.Errorf("%s: save state: %w", step.Name(), err)
			}
			scoped.Debug("state snapshot saved")
		}
	}

	logger.Info("all steps complete")
	return st, nil
}
