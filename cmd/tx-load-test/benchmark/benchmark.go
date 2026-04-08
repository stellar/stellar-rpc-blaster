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
	"golang.org/x/sync/errgroup"

	"github.com/stellar/go-stellar-sdk/keypair"
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
	// VerifyReady performs runtime ledger/state checks specific to the mode and
	// returns an actionable error if the persisted state is not benchmark-ready.
	VerifyReady(ctx context.Context, state *state.State) error
	// NewTargeter validates the required state and returns a Vegeta Targeter
	// that emits one signed RPC request per call, plus an optional
	// SequenceResetFunc for reverting sequence numbers on non-consuming
	// failures.  Modes that do not manage sequences may return nil.
	NewTargeter(ctx context.Context, rpcURL string, state *state.State, txSourceAccounts []*keypair.Full) (vegeta.Targeter, SequenceResetFunc, error)
}

// modes maps each config.BenchmarkMode string to its Mode implementation.
var modes = map[config.BenchmarkMode]Mode{
	config.ModeSACTransfer: sacTransferMode{},
	config.ModeOZTransfer:  ozTransferMode{},
	config.ModeSoroswap:    soroswapMode{},
}

var supportedModes = []config.BenchmarkMode{
	config.ModeSACTransfer,
	config.ModeOZTransfer,
	config.ModeSoroswap,
}

func verifyReadyForModes(ctx context.Context, st *state.State, modeSet map[config.BenchmarkMode]Mode, modeOrder []config.BenchmarkMode) error {
	for _, modeName := range modeOrder {
		mode, ok := modeSet[modeName]
		if !ok {
			return fmt.Errorf("missing benchmark mode implementation for %q", modeName)
		}
		if err := mode.VerifyReady(ctx, st); err != nil {
			return fmt.Errorf("mode=%s: %w", mode.Label(), err)
		}
	}
	return nil
}

type workload struct {
	label       string
	targetRPS   int
	rateSummary string
	targeter    vegeta.Targeter
	resetSeq    SequenceResetFunc
}

// ValidateConfig checks that the benchmark configuration is internally
// consistent.  Call this before the expensive setup phase so that obvious
// misconfigurations are caught immediately.
func ValidateConfig(cfg config.Config) error {
	if _, ok := modes[cfg.Mode]; !ok {
		return fmt.Errorf("unknown benchmark mode: %q", cfg.Mode)
	}
	if cfg.TargetRPS <= 0 {
		return fmt.Errorf("target-rps must be > 0")
	}
	if cfg.ClassicRPS < 0 {
		return fmt.Errorf("classic-rps must be >= 0")
	}
	if cfg.ClassicRPS%state.SimplePaymentOpsPerTransaction != 0 {
		return fmt.Errorf(
			"classic-rps must be a multiple of %d when simple payments use fixed-size batches",
			state.SimplePaymentOpsPerTransaction,
		)
	}

	simpleRequired := state.SimplePaymentSourceAccountCount(cfg)
	sorobanRequired := state.SorobanSourceAccountCount(cfg)
	totalRequired := simpleRequired + sorobanRequired

	if cfg.NumberOfAccounts < totalRequired {
		return fmt.Errorf(
			"account pool too small for configured benchmark: have %d accounts but need at least %d "+
				"(%d Soroban tx sources + %d simple-payment tx sources)  -- increase --accounts, reduce --target-rps, reduce --classic-rps, or shorten --duration",
			cfg.NumberOfAccounts, totalRequired, sorobanRequired, simpleRequired,
		)
	}
	return nil
}

// ValidateSetupConfig checks that a single setup run can support every
// benchmark mode for the requested rates, duration, and account pool size.
func ValidateSetupConfig(cfg config.Config) error {
	for _, mode := range supportedModes {
		modeCfg := cfg
		modeCfg.Mode = mode
		if err := ValidateConfig(modeCfg); err != nil {
			return fmt.Errorf("benchmark shape is not valid for mode=%s: %w", mode, err)
		}
	}
	return nil
}

// VerifySetupReadyForAllModes performs runtime ledger/state checks for every
// supported benchmark mode after setup completes. This catches readiness
// issues during setup instead of failing later at bench startup.
func VerifySetupReadyForAllModes(ctx context.Context, st *state.State) error {
	return verifyReadyForModes(ctx, st, modes, supportedModes)
}

func holderAccountsForBenchmark(cfg config.Config, st *state.State) ([]*keypair.Full, error) {
	holderRequired := state.HolderAccountCount(cfg)
	if holderRequired == 0 {
		return nil, nil
	}

	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holderAccounts) < holderRequired {
		return nil, fmt.Errorf(
			"setup has only %d holder accounts but benchmark needs at least %d for mode=%s  -- rerun setup with enough --accounts to provision more trustlined accounts",
			len(holderAccounts), holderRequired, cfg.Mode,
		)
	}
	return holderAccounts, nil
}

func partitionSourceAccounts(cfg config.Config, st *state.State) ([]*keypair.Full, []*keypair.Full, error) {
	simpleSourceCount := state.SimplePaymentSourceAccountCount(cfg)
	partitionIdx := len(st.AccountKPs) - simpleSourceCount
	simpleSources := st.AccountKPs[partitionIdx:]
	sorobanPool := st.AccountKPs[:partitionIdx]
	sorobanRequired := state.SorobanSourceAccountCount(cfg)
	if len(sorobanPool) < sorobanRequired {
		return nil, nil, fmt.Errorf(
			"need at least %d Soroban tx-source accounts after reserving %d simple-payment sources, got %d  -- rerun setup with more --accounts or lower the configured rates/duration",
			sorobanRequired, simpleSourceCount, len(sorobanPool),
		)
	}
	sorobanSources := sorobanPool[:sorobanRequired]

	return simpleSources, sorobanSources, nil
}

func runWorkloads(ctx context.Context, logger *log.Entry, baseCfg config.Config, st *state.State, httpClient *http.Client, workloads []workload) error {
	g, groupCtx := errgroup.WithContext(ctx)
	for _, workload := range workloads {
		wl := workload
		g.Go(func() error {
			runCfg := baseCfg
			runCfg.TargetRPS = wl.targetRPS
			scoped := logger.WithField("phase", wl.label)
			scoped.Infof("duration=%s ramp=%s %s", runCfg.Duration, runCfg.RampUp, wl.rateSummary)
			if err := runVegetaAttack(groupCtx, scoped, runCfg, httpClient, st.RPCClient, wl.label, wl.targeter, wl.resetSeq); err != nil {
				return fmt.Errorf("%s: %w", wl.label, err)
			}
			return nil
		})
	}

	return g.Wait()
}

// Run executes the benchmark phase using the mode chosen in cfg.
// It blocks until the benchmark duration has elapsed or ctx is cancelled.
func Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State, httpClient *http.Client) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	if _, err := holderAccountsForBenchmark(cfg, st); err != nil {
		return err
	}
	simpleSources, sorobanSources, err := partitionSourceAccounts(cfg, st)
	if err != nil {
		return err
	}

	m := modes[cfg.Mode]
	if err := m.VerifyReady(ctx, st); err != nil {
		return fmt.Errorf("%s: verify ready: %w", m.Label(), err)
	}
	targeter, resetSeq, err := m.NewTargeter(ctx, cfg.RPCURL, st, sorobanSources)
	if err != nil {
		return fmt.Errorf("%s: build targeter: %w", m.Label(), err)
	}

	workloads := []workload{{
		label:       m.Label(),
		targetRPS:   cfg.TargetRPS,
		rateSummary: fmt.Sprintf("targetRPS=%d tx/s", cfg.TargetRPS),
		targeter:    targeter,
		resetSeq:    resetSeq,
	}}
	if cfg.ClassicRPS > 0 {
		simpleTargeter, simpleResetSeq, err := newSimplePaymentTargeter(ctx, cfg.RPCURL, st, simpleSources, st.AccountKPs)
		if err != nil {
			return fmt.Errorf("simple-payment: build targeter: %w", err)
		}
		workloads = append(workloads, workload{
			label:       "simple-payment",
			targetRPS:   state.SimplePaymentTransactionRate(cfg),
			rateSummary: fmt.Sprintf("targetOps=%d ops/s targetRPS=%d tx/s opsPerTx=%d", cfg.ClassicRPS, state.SimplePaymentTransactionRate(cfg), state.SimplePaymentOpsPerTransaction),
			targeter:    simpleTargeter,
			resetSeq:    simpleResetSeq,
		})
	}
	return runWorkloads(ctx, logger, cfg, st, httpClient, workloads)
}
