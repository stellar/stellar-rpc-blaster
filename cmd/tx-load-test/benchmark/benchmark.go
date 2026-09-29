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
	// that emits one signed RPC request per call. Transaction source accounts,
	// globally unique request IDs, and sequence assignment are coordinated by the
	// shared account manager.
	NewTargeter(ctx context.Context, rpcURL string, state *state.State, accounts accountLeaseManager) (vegeta.Targeter, error)
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
	newTargeter func(context.Context) (vegeta.Targeter, error)
}

// ValidateConfig checks that the benchmark configuration is internally
// consistent.  Call this before the expensive setup phase so that obvious
// misconfigurations are caught immediately.
func ValidateConfig(cfg config.Config) error {
	if err := ValidateCLIConfig(cfg); err != nil {
		return err
	}

	totalRequired := state.AnySourceAccountCount(cfg)

	if cfg.NumberOfAccounts < totalRequired {
		return fmt.Errorf(
			"account pool too small for configured benchmark: have %d accounts but need at least %d "+
				"for the shared tx-source pool  -- increase --accounts, reduce --target-rps, reduce --classic-rps, or shorten --duration",
			cfg.NumberOfAccounts, totalRequired,
		)
	}
	return nil
}

// ValidateCLIConfig checks benchmark settings that can be validated before any
// state-file load or RPC interaction.
func ValidateCLIConfig(cfg config.Config) error {
	if _, ok := modes[cfg.Mode]; !ok {
		return fmt.Errorf("unknown benchmark mode: %q", cfg.Mode)
	}
	if cfg.TargetRPS <= 0 {
		return fmt.Errorf("target-rps must be > 0")
	}
	if cfg.ClassicRPS < 0 {
		return fmt.Errorf("classic-rps must be >= 0")
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

func runWorkloads(ctx context.Context, logger *log.Entry, baseCfg config.Config, st *state.State, httpClient *http.Client, accounts accountLeaseManager, workloads []workload) error {
	recorder, err := openBenchmarkTraceRecorder(baseCfg.TraceFile)
	if err != nil {
		return err
	}
	defer func() {
		if recorder != nil {
			_ = recorder.Close()
		}
	}()

	g, groupCtx := errgroup.WithContext(ctx)
	for _, workload := range workloads {
		wl := workload
		g.Go(func() error {
			runCfg := baseCfg
			runCfg.TargetRPS = wl.targetRPS
			scoped := logger.WithField("phase", wl.label)
			scoped.Infof("duration=%s ramp=%s %s", runCfg.Duration, runCfg.RampUp, wl.rateSummary)
			if err := runVegetaAttack(groupCtx, scoped, runCfg, httpClient, st.RPCClient, wl.label, wl.newTargeter, accounts, recorder); err != nil {
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
	if st == nil || st.FeePayerKP == nil {
		return fmt.Errorf("benchmark state missing fee payer keypair")
	}
	if _, err := holderAccountsForBenchmark(cfg, st); err != nil {
		return err
	}
	accounts, err := newAccountManager(ctx, st)
	if err != nil {
		return err
	}

	m := modes[cfg.Mode]
	if err := m.VerifyReady(ctx, st); err != nil {
		return fmt.Errorf("%s: verify ready: %w", m.Label(), err)
	}

	workloads := []workload{{
		label:       m.Label(),
		targetRPS:   cfg.TargetRPS,
		rateSummary: fmt.Sprintf("targetRPS=%d tx/s", cfg.TargetRPS),
		newTargeter: func(runCtx context.Context) (vegeta.Targeter, error) {
			return m.NewTargeter(runCtx, cfg.RPCURL, st, accounts)
		},
	}}
	if cfg.ClassicRPS > 0 {
		workloads = append(workloads, workload{
			label:       "simple-payment",
			targetRPS:   state.SimplePaymentTransactionRate(cfg),
			rateSummary: fmt.Sprintf("targetRPS=%d tx/s paymentOpsPerTx=%d", state.SimplePaymentTransactionRate(cfg), state.SimplePaymentOpsPerTransaction),
			newTargeter: func(runCtx context.Context) (vegeta.Targeter, error) {
				return newSimplePaymentTargeter(runCtx, cfg.RPCURL, st, accounts, st.AccountKPs)
			},
		})
	}
	return runWorkloads(ctx, logger, cfg, st, httpClient, accounts, workloads)
}
