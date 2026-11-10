package setup

import (
	"context"
	"fmt"
	"time"

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
		soroswapCoreStep{},
		soroswapPoolsStep{},
		liquidityStep{},
		ozTokenStep{},
	}
	return steps
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
	setupStarted := time.Now()
	for i, step := range steps {
		scoped := logger.WithField("phase", step.Name())
		stepStarted := time.Now()
		scoped.Infof("step %d/%d started", i+1, len(steps))
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
			scoped.Infof("state snapshot saved after %s", time.Since(stepStarted).Round(time.Millisecond))
		}
		scoped.Infof("step %d/%d complete in %s", i+1, len(steps), time.Since(stepStarted).Round(time.Millisecond))
	}

	logger.Infof("all steps complete in %s", time.Since(setupStarted).Round(time.Millisecond))
	return st, nil
}
