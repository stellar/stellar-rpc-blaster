// Package benchmark drives the load-test phase of tx-load-test.
//
// It re-uses the RampToConstantPacer from the blaster engine so that RPS
// increases linearly from 1 to TargetRPS over the configured ramp-up window,
// then holds constant for the remainder of the benchmark duration.
package benchmark

import (
	"context"
	"fmt"
	"net/http"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// SequenceResetFunc is called when a submitted transaction was rejected
// without consuming its on-ledger sequence number (e.g. TRY_AGAIN_LATER or
// ERROR).  The argument is the JSON-RPC ID echoed in the response, which the
// implementation uses to identify the source account and revert its sequence
// counter so the next transaction for that account retries with the correct
// sequence.
type SequenceResetFunc func(jsonRPCId int64)

// Mode is a single benchmark workload. Each implementation knows how to
// build a Vegeta Targeter and provides a short label used in logging and
// as the Vegeta attack name.
type Mode interface {
	// Label returns a short, stable identifier (e.g. "sac-transfer").
	Label() string
	// NewTargeter validates the required state and returns a Vegeta Targeter
	// that emits one signed RPC request per call, plus an optional
	// SequenceResetFunc for reverting sequence numbers on non-consuming
	// failures.  Modes that do not manage sequences may return nil.
	NewTargeter(ctx context.Context, rpcURL string, state *state.State) (vegeta.Targeter, SequenceResetFunc, error)
}

// modes maps each config.BenchmarkMode string to its Mode implementation.
// Soroswap remains intentionally omitted until that workload is implemented.
var modes = map[config.BenchmarkMode]Mode{
	config.ModeSACTransfer: sacTransferMode{},
	config.ModeOZTransfer:  ozTransferMode{},
}

// ledgerCloseSeconds is the target ledger close time used when validating that
// the account pool is large enough to avoid within-ledger sequence collisions.
const ledgerCloseSeconds = 5

// ValidateConfig checks that the benchmark configuration is internally
// consistent.  Call this before the expensive setup phase so that obvious
// misconfigurations are caught immediately.
func ValidateConfig(cfg config.Config) error {
	if _, ok := modes[cfg.Mode]; !ok {
		return fmt.Errorf("unknown benchmark mode: %q", cfg.Mode)
	}

	// Each account can only be the source of one transaction per ledger.
	// The hard minimum prevents within-ledger sequence collisions:
	//   NumberOfAccounts >= TargetRPS * ledgerCloseSeconds
	hardMin := cfg.TargetRPS * ledgerCloseSeconds
	if cfg.NumberOfAccounts < hardMin {
		return fmt.Errorf(
			"account pool too small for target RPS: have %d accounts but need at least %d "+
				"(%d RPS * %d s ledger) to avoid within-ledger sequence collisions  -- "+
				"increase --accounts or reduce --target-rps",
			cfg.NumberOfAccounts, hardMin, cfg.TargetRPS, ledgerCloseSeconds,
		)
	}

	// The recommended minimum is 2x the hard minimum.  This gives each
	// account a reuse interval of at least 2 ledgers, ensuring the previous
	// transaction is confirmed on-chain before the next one is built.
	// Without this margin, mempool evictions cascade into BadSeq errors
	// because the poll workers cannot detect the eviction before the
	// account is reused.
	recommendedMin := hardMin * 2
	if cfg.NumberOfAccounts < recommendedMin {
		log.New().Warnf(
			"account pool is below recommended size: have %d accounts, recommend at least %d "+
				"(%d RPS * %d s ledger * 2) to avoid sequence errors from mempool evictions  -- "+
				"increase --accounts for cleaner runs",
			cfg.NumberOfAccounts, recommendedMin, cfg.TargetRPS, ledgerCloseSeconds,
		)
	}
	return nil
}

// Run executes the benchmark phase using the mode chosen in cfg.
// It blocks until the benchmark duration has elapsed or ctx is cancelled.
func Run(ctx context.Context, logger *log.Entry, cfg config.Config, state *state.State, httpClient *http.Client) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}

	m := modes[cfg.Mode]
	targeter, resetSeq, err := m.NewTargeter(ctx, cfg.RPCURL, state)
	if err != nil {
		return fmt.Errorf("%s: build targeter: %w", m.Label(), err)
	}

	scoped := logger.WithField("phase", m.Label())
	scoped.Infof("duration=%s ramp=%s targetRPS=%d", cfg.Duration, cfg.RampUp, cfg.TargetRPS)
	return runVegetaAttack(ctx, scoped, cfg, httpClient, state.RPCClient, m.Label(), targeter, resetSeq)
}
