package benchmark

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	sharedsoroswap "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroswap"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const RestoreModeAll = "all"

const restoreMaxLoggedErrors = 5
const defaultRestoreProgressInterval = 100

type RestoreOptions struct {
	Mode         string
	DryRun       bool
	Verify       bool
	AccountStart int
	AccountLimit int
	// ProgressInterval controls how often long restore scans log progress. A
	// value of 0 uses the default; a negative value disables periodic progress.
	ProgressInterval int
}

type restoreSummary struct {
	mode                config.BenchmarkMode
	dryRun              bool
	accountStart        int
	accountEnd          int
	selectedAccounts    int
	probes              int
	restoreNeededProbes int
	restoreTransactions int
	noopProbes          int
	readOnlyKeys        int
	readWriteKeys       int
	resourceFee         xdr.Int64
	// autoRestoreProbes counts probes whose simulator reported no legacy
	// RestorePreamble but did emit SorobanResourcesExtV0.ArchivedSorobanEntries
	// (protocol-23+ autorestore). These probes don't submit a separate
	// RestoreFootprint -- the invoking tx pays for inline restoration via the
	// extension -- but they ARE indicative that read-write contract data is
	// archived. Splitting them out keeps restoreTransactions honest while
	// surfacing the count operators need to see.
	autoRestoreProbes int
	autoRestoreKeys   int
	totalProbes       int
	progressInterval  int
	logger            *log.Entry
	startedAt         time.Time
	errors            []error
}

type restoreProbeFunc func(
	context.Context,
	*state.State,
	*keypair.Full,
	string,
	xdr.InvokeContractArgs,
	int64,
	sharedsoroban.RestoreProbeOptions,
) (sharedsoroban.RestoreProbeResult, error)

var restoreInvokeContract = sharedsoroban.RestoreInvokeContract
var restoreSoroswapTokenBalance = sharedsoroswap.TokenBalance

type soroswapRestoreProbe struct {
	inputToken  string
	outputToken string
	amountIn    int64
}

type soroswapBenchmarkValidationProbe struct {
	pairIndex   int
	direction   string
	inputToken  string
	outputToken string
	inputAsset  xdr.Asset
	outputAsset xdr.Asset
	amountIn    int64
}

type soroswapBenchmarkFootprintValidationSummary struct {
	dryRun                    bool
	accountStart              int
	accountEnd                int
	selectedAccounts          int
	templateProbes            int
	templates                 int
	benchmarkProbes           int
	totalBenchmarkProbes      int
	restoreNeededProbes       int
	footprintMismatches       int
	missingReadOnlyKeys       int
	missingReadWriteKeys      int
	unexpectedReadOnlyKeys    int
	unexpectedReadWriteKeys   int
	allowedExtraReadWriteKeys int
	skippedBenchmarkProbes    int
	progressInterval          int
	logger                    *log.Entry
	startedAt                 time.Time
	errorCount                int
	errors                    []error
}

type soroswapFootprintComparison struct {
	missingReadOnlyKeys       int
	missingReadWriteKeys      int
	unexpectedReadOnlyKeys    int
	unexpectedReadWriteKeys   int
	allowedExtraReadWriteKeys int
}

type encodedFootprint struct {
	readOnly  map[string]xdr.LedgerKey
	readWrite map[string]xdr.LedgerKey
	any       map[string]xdr.LedgerKey
}

func RestoreArchivedState(ctx context.Context, logger *log.Entry, st *state.State, options RestoreOptions) error {
	if st == nil || st.FeePayerKP == nil {
		return fmt.Errorf("restore state missing fee payer keypair")
	}
	if options.AccountStart < 0 {
		return fmt.Errorf("account-start must be >= 0")
	}
	if options.AccountLimit < 0 {
		return fmt.Errorf("account-limit must be >= 0")
	}

	modesToRestore, err := restoreModes(options.Mode)
	if err != nil {
		return err
	}

	var errs []error
	for _, mode := range modesToRestore {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		summary, err := restoreMode(ctx, logger, st, mode, options)
		logRestoreSummary(logger, summary)
		modeErr := err
		if modeErr == nil && mode == config.ModeSoroswap && ctx.Err() == nil {
			validationSummary, validationErr := validateSoroswapBenchmarkFootprints(ctx, logger, st, options)
			logSoroswapBenchmarkFootprintValidationSummary(logger, validationSummary)
			if validationErr != nil {
				modeErr = fmt.Errorf("benchmark footprint validation: %w", validationErr)
			}
		}
		if modeErr != nil {
			errs = append(errs, fmt.Errorf("mode=%s: %w", mode, modeErr))
		}
		if options.Verify && modeErr == nil && !options.DryRun && ctx.Err() == nil {
			verifyOptions := options
			verifyOptions.DryRun = true
			verifySummary, verifyErr := restoreMode(ctx, logger, st, mode, verifyOptions)
			verifySummary.dryRun = false
			logRestoreVerifySummary(logger, verifySummary)
			if verifyErr != nil {
				errs = append(errs, fmt.Errorf("mode=%s verify: %w", mode, verifyErr))
			} else if remaining := verifySummary.restoreNeededProbes - verifySummary.autoRestoreProbes; remaining > 0 {
				// Autorestore probes intentionally aren't followed by a
				// RestoreFootprint tx -- the bench's invocations will pay
				// for inline restoration via SorobanResourcesExtV0. So they
				// will keep showing up on every verify pass and aren't an
				// error here. Only legacy RestorePreamble probes that still
				// require restoration after a real restore run are.
				errs = append(errs, fmt.Errorf("mode=%s verify: %d probes still require restore", mode, remaining))
			}
		}
	}
	return errors.Join(errs...)
}

func restoreModes(mode string) ([]config.BenchmarkMode, error) {
	if mode == "" || mode == RestoreModeAll {
		return append([]config.BenchmarkMode(nil), supportedModes...), nil
	}
	benchmarkMode := config.BenchmarkMode(mode)
	if _, ok := modes[benchmarkMode]; !ok {
		return nil, fmt.Errorf("unknown restore mode: %q", mode)
	}
	return []config.BenchmarkMode{benchmarkMode}, nil
}

func restoreMode(ctx context.Context, logger *log.Entry, st *state.State, mode config.BenchmarkMode, options RestoreOptions) (restoreSummary, error) {
	summary := restoreSummary{
		mode:             mode,
		dryRun:           options.DryRun,
		progressInterval: normalizeRestoreProgressInterval(options.ProgressInterval),
		logger:           logger,
		startedAt:        time.Now(),
	}
	switch mode {
	case config.ModeSACTransfer:
		return restoreSACContracts(ctx, st, options, summary)
	case config.ModeOZTransfer:
		return restoreOZBalances(ctx, st, options, summary)
	case config.ModeSoroswap:
		return restoreSoroswap(ctx, st, options, summary)
	default:
		summary.addError(fmt.Errorf("unknown restore mode: %q", mode))
		return summary, summary.error()
	}
}

func restoreSACContracts(ctx context.Context, st *state.State, options RestoreOptions, summary restoreSummary) (restoreSummary, error) {
	if len(st.SACs) == 0 {
		summary.addError(fmt.Errorf("state has no SAC contracts"))
		return summary, summary.error()
	}
	holders := st.SACHolderKPs
	if len(holders) == 0 {
		holders = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holders) < 2 {
		summary.addError(fmt.Errorf("need at least 2 holder accounts for SAC restore, got %d", len(holders)))
		return summary, summary.error()
	}
	accounts, start, end, err := selectAccountRange(holders, options.AccountStart, options.AccountLimit)
	if err != nil {
		summary.addError(err)
		return summary, summary.error()
	}
	summary.accountStart = start
	summary.accountEnd = end
	summary.selectedAccounts = len(accounts)
	summary.totalProbes = len(accounts) * len(st.SACs)
	summary.logStart("restore mode started")
	for _, srcKP := range accounts {
		dstKP := firstDifferentAccount(holders, srcKP.Address())
		if dstKP == nil {
			summary.addError(fmt.Errorf("no destination holder account available for %s", srcKP.Address()))
			continue
		}
		srcAccID, err := xdr.AddressToAccountId(srcKP.Address())
		if err != nil {
			summary.addError(fmt.Errorf("parse SAC restore source account %s: %w", srcKP.Address(), err))
			continue
		}
		dstAccID, err := xdr.AddressToAccountId(dstKP.Address())
		if err != nil {
			summary.addError(fmt.Errorf("parse SAC restore destination account %s: %w", dstKP.Address(), err))
			continue
		}
		for i, contract := range st.SACs {
			if contract == "" {
				summary.addError(fmt.Errorf("missing SAC contract[%d]", i))
				continue
			}
			contractID, err := ledger.DecodeContractID(contract)
			if err != nil {
				summary.addError(fmt.Errorf("decode SAC contract[%d]: %w", i, err))
				continue
			}
			if !summary.recordProbe(ctx, st, srcKP, srcKP.Address(), buildSACTransferInvokeArgs(contractID, srcAccID, dstAccID)) {
				return summary, summary.error()
			}
		}
	}
	return summary, summary.error()
}

func restoreOZBalances(ctx context.Context, st *state.State, options RestoreOptions, summary restoreSummary) (restoreSummary, error) {
	if st.OZTokenContract == "" {
		summary.addError(fmt.Errorf("state missing OZ token contract"))
		return summary, summary.error()
	}
	if len(st.AccountKPs) < 2 {
		summary.addError(fmt.Errorf("need at least 2 participant accounts for OZ restore, got %d", len(st.AccountKPs)))
		return summary, summary.error()
	}
	contractID, err := ledger.DecodeContractID(st.OZTokenContract)
	if err != nil {
		summary.addError(fmt.Errorf("decode OZ token contract ID: %w", err))
		return summary, summary.error()
	}
	accounts, start, end, err := selectAccountRange(st.AccountKPs, options.AccountStart, options.AccountLimit)
	if err != nil {
		summary.addError(err)
		return summary, summary.error()
	}
	summary.accountStart = start
	summary.accountEnd = end
	summary.selectedAccounts = len(accounts)
	summary.totalProbes = len(accounts)
	summary.logStart("restore mode started")
	for _, srcKP := range accounts {
		dstKP := firstDifferentAccount(st.AccountKPs, srcKP.Address())
		if dstKP == nil {
			summary.addError(fmt.Errorf("no destination account available for %s", srcKP.Address()))
			continue
		}
		srcAccID, err := xdr.AddressToAccountId(srcKP.Address())
		if err != nil {
			summary.addError(fmt.Errorf("parse OZ source account %s: %w", srcKP.Address(), err))
			continue
		}
		dstAccID, err := xdr.AddressToAccountId(dstKP.Address())
		if err != nil {
			summary.addError(fmt.Errorf("parse OZ destination account %s: %w", dstKP.Address(), err))
			continue
		}
		if !summary.recordProbe(ctx, st, srcKP, srcKP.Address(), buildOZTransferInvokeArgs(contractID, srcAccID, dstAccID)) {
			return summary, summary.error()
		}
	}
	return summary, summary.error()
}

func restoreSoroswap(ctx context.Context, st *state.State, options RestoreOptions, summary restoreSummary) (restoreSummary, error) {
	if st.SoroswapFactoryContract == "" {
		summary.addError(fmt.Errorf("state missing Soroswap factory contract"))
		return summary, summary.error()
	}
	if st.SoroswapRouterContract == "" {
		summary.addError(fmt.Errorf("state missing Soroswap router contract"))
		return summary, summary.error()
	}
	if len(st.SoroswapPairContracts) != len(sharedsoroswap.BenchmarkPairs) {
		summary.addError(fmt.Errorf("need %d Soroswap pair contracts, got %d", len(sharedsoroswap.BenchmarkPairs), len(st.SoroswapPairContracts)))
		return summary, summary.error()
	}
	routerID, err := ledger.DecodeContractID(st.SoroswapRouterContract)
	if err != nil {
		summary.addError(fmt.Errorf("decode Soroswap router contract ID: %w", err))
		return summary, summary.error()
	}
	holders := soroswapHolderAccounts(st)
	if len(holders) == 0 {
		summary.addError(fmt.Errorf("need at least 1 holder account for Soroswap restore"))
		return summary, summary.error()
	}
	accounts, start, end, err := selectAccountRange(holders, options.AccountStart, options.AccountLimit)
	if err != nil {
		summary.addError(err)
		return summary, summary.error()
	}
	summary.accountStart = start
	summary.accountEnd = end
	summary.selectedAccounts = len(accounts)
	summary.totalProbes = len(accounts) * len(sharedsoroswap.BenchmarkPairs) * 2
	summary.logStart("restore mode started")
	deadline := uint64(time.Now().Add(soroswapSwapDeadlineWindow).Unix())
	probes := make([]soroswapRestoreProbe, 0, len(sharedsoroswap.BenchmarkPairs)*2)

	for i, pair := range sharedsoroswap.BenchmarkPairs {
		if err := ctx.Err(); err != nil {
			summary.addError(err)
			return summary, summary.error()
		}
		pairContract := st.SoroswapPairContracts[i]
		tokenA := st.SACs[pair[0]]
		tokenB := st.SACs[pair[1]]
		if tokenA == "" || tokenB == "" || pairContract == "" {
			summary.addError(fmt.Errorf("soroswap pool %d is missing token or pair contract state", i))
			continue
		}
		reserveA, err := restoreSoroswapTokenBalance(ctx, st, tokenA, pairContract)
		if err != nil {
			summary.addError(fmt.Errorf("pool %d reserve A: %w", i, err))
			continue
		}
		reserveB, err := restoreSoroswapTokenBalance(ctx, st, tokenB, pairContract)
		if err != nil {
			summary.addError(fmt.Errorf("pool %d reserve B: %w", i, err))
			continue
		}
		probes = append(probes,
			soroswapRestoreProbe{inputToken: tokenA, outputToken: tokenB, amountIn: sharedsoroswap.SwapAmount(reserveA)},
			soroswapRestoreProbe{inputToken: tokenB, outputToken: tokenA, amountIn: sharedsoroswap.SwapAmount(reserveB)},
		)
	}
	if len(probes) == 0 {
		if summary.error() == nil {
			summary.addError(fmt.Errorf("no Soroswap restore probes available"))
		}
		return summary, summary.error()
	}
	summary.totalProbes = len(accounts) * len(probes)
	for _, trader := range accounts {
		for _, probe := range probes {
			if !summary.recordSoroswapProbe(ctx, st, routerID, trader, probe, deadline) {
				return summary, summary.error()
			}
		}
	}
	return summary, summary.error()
}

func soroswapHolderAccounts(st *state.State) []*keypair.Full {
	holders := st.SACHolderKPs
	if len(holders) == 0 {
		holders = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	return holders
}

func validateSoroswapBenchmarkFootprints(ctx context.Context, logger *log.Entry, st *state.State, options RestoreOptions) (soroswapBenchmarkFootprintValidationSummary, error) {
	summary := soroswapBenchmarkFootprintValidationSummary{
		dryRun:           options.DryRun,
		progressInterval: normalizeRestoreProgressInterval(options.ProgressInterval),
		logger:           logger,
		startedAt:        time.Now(),
	}
	if st.SoroswapRouterContract == "" {
		summary.addError(fmt.Errorf("state missing Soroswap router contract"))
		return summary, summary.error()
	}
	if len(st.SoroswapPairContracts) != len(sharedsoroswap.BenchmarkPairs) {
		summary.addError(fmt.Errorf("need %d Soroswap pair contracts, got %d", len(sharedsoroswap.BenchmarkPairs), len(st.SoroswapPairContracts)))
		return summary, summary.error()
	}
	routerID, err := ledger.DecodeContractID(st.SoroswapRouterContract)
	if err != nil {
		summary.addError(fmt.Errorf("decode Soroswap router contract ID: %w", err))
		return summary, summary.error()
	}
	holders := soroswapHolderAccounts(st)
	if len(holders) == 0 {
		summary.addError(fmt.Errorf("need at least 1 holder account for Soroswap benchmark footprint validation"))
		return summary, summary.error()
	}
	accounts, start, end, err := selectAccountRange(holders, options.AccountStart, options.AccountLimit)
	if err != nil {
		summary.addError(err)
		return summary, summary.error()
	}
	summary.accountStart = start
	summary.accountEnd = end
	summary.selectedAccounts = len(accounts)

	probes := buildSoroswapBenchmarkValidationProbes(ctx, st, &summary)
	summary.totalBenchmarkProbes = len(accounts) * len(probes)
	summary.logStart("soroswap benchmark footprint validation started")
	if len(probes) == 0 {
		if summary.error() == nil {
			summary.addError(fmt.Errorf("no Soroswap benchmark footprint validation probes available"))
		}
		return summary, summary.error()
	}

	deadline := uint64(time.Now().Add(soroswapSwapDeadlineWindow).Unix())
	representativeTrader := holders[0]
	templates := make([]soroswapSwapTemplate, 0, len(probes))
	for _, probe := range probes {
		tmpl, ok := summary.recordSoroswapBenchmarkTemplate(ctx, st, routerID, representativeTrader, probe, deadline)
		if ok {
			templates = append(templates, tmpl)
		}
		if err := ctx.Err(); err != nil {
			summary.addError(err)
			return summary, summary.error()
		}
	}
	summary.templates = len(templates)
	summary.skippedBenchmarkProbes = len(accounts) * (len(probes) - len(templates))
	if len(templates) == 0 {
		return summary, summary.error()
	}

	for _, trader := range accounts {
		for templateIdx, tmpl := range templates {
			if !summary.recordSoroswapBenchmarkFootprint(ctx, st, trader, tmpl, templateIdx) {
				return summary, summary.error()
			}
		}
	}
	return summary, summary.error()
}

func buildSoroswapBenchmarkValidationProbes(ctx context.Context, st *state.State, summary *soroswapBenchmarkFootprintValidationSummary) []soroswapBenchmarkValidationProbe {
	probes := make([]soroswapBenchmarkValidationProbe, 0, len(sharedsoroswap.BenchmarkPairs)*2)
	for i, pair := range sharedsoroswap.BenchmarkPairs {
		if err := ctx.Err(); err != nil {
			summary.addError(err)
			return probes
		}
		pairContract := st.SoroswapPairContracts[i]
		tokenA := st.SACs[pair[0]]
		tokenB := st.SACs[pair[1]]
		if tokenA == "" || tokenB == "" || pairContract == "" {
			summary.addError(fmt.Errorf("soroswap pool %d is missing token or pair contract state", i))
			continue
		}
		assetA, err := st.Assets[pair[0]].ToXDR()
		if err != nil {
			summary.addError(fmt.Errorf("pool %d asset A to XDR: %w", i, err))
			continue
		}
		assetB, err := st.Assets[pair[1]].ToXDR()
		if err != nil {
			summary.addError(fmt.Errorf("pool %d asset B to XDR: %w", i, err))
			continue
		}
		reserveA, err := restoreSoroswapTokenBalance(ctx, st, tokenA, pairContract)
		if err != nil {
			summary.addError(fmt.Errorf("pool %d reserve A: %w", i, err))
			continue
		}
		reserveB, err := restoreSoroswapTokenBalance(ctx, st, tokenB, pairContract)
		if err != nil {
			summary.addError(fmt.Errorf("pool %d reserve B: %w", i, err))
			continue
		}
		probes = append(probes,
			soroswapBenchmarkValidationProbe{
				pairIndex:   i,
				direction:   "A->B",
				inputToken:  tokenA,
				outputToken: tokenB,
				inputAsset:  assetA,
				outputAsset: assetB,
				amountIn:    sharedsoroswap.SwapAmount(reserveA),
			},
			soroswapBenchmarkValidationProbe{
				pairIndex:   i,
				direction:   "B->A",
				inputToken:  tokenB,
				outputToken: tokenA,
				inputAsset:  assetB,
				outputAsset: assetA,
				amountIn:    sharedsoroswap.SwapAmount(reserveB),
			},
		)
	}
	return probes
}

func (s *soroswapBenchmarkFootprintValidationSummary) recordSoroswapBenchmarkTemplate(
	ctx context.Context,
	st *state.State,
	routerID xdr.ContractId,
	representativeTrader *keypair.Full,
	probe soroswapBenchmarkValidationProbe,
	deadline uint64,
) (soroswapSwapTemplate, bool) {
	if err := ctx.Err(); err != nil {
		s.addError(err)
		return soroswapSwapTemplate{}, false
	}
	s.templateProbes++
	invokeArgs, err := sharedsoroswap.BuildSwapInvokeArgs(routerID, representativeTrader.Address(), probe.inputToken, probe.outputToken, probe.amountIn, deadline)
	if err != nil {
		s.addError(fmt.Errorf("build Soroswap benchmark template probe pair=%d direction=%s: %w", probe.pairIndex, probe.direction, err))
		return soroswapSwapTemplate{}, false
	}
	result, err := restoreInvokeContract(ctx, st, representativeTrader, representativeTrader.Address(), invokeArgs, benchmarkBaseFeeMin, sharedsoroban.RestoreProbeOptions{
		DryRun:    true,
		PadFactor: resourcePadFactor,
	})
	if err != nil {
		s.addError(fmt.Errorf("simulate Soroswap benchmark template pair=%d direction=%s: %w", probe.pairIndex, probe.direction, err))
		return soroswapSwapTemplate{}, false
	}
	if result.RestoreNeeded {
		s.restoreNeededProbes++
		return soroswapSwapTemplate{}, false
	}
	if !result.HasSimulation {
		s.addError(fmt.Errorf("simulate Soroswap benchmark template pair=%d direction=%s returned no simulation", probe.pairIndex, probe.direction))
		return soroswapSwapTemplate{}, false
	}
	return soroswapSwapTemplate{
		traderAddress: representativeTrader.Address(),
		inputAsset:    probe.inputAsset,
		outputAsset:   probe.outputAsset,
		simulatedInvocationTemplate: simulatedInvocationTemplate{
			invokeArgs: invokeArgs,
			simulation: result.Simulation,
		},
	}, true
}

func (s *soroswapBenchmarkFootprintValidationSummary) recordSoroswapBenchmarkFootprint(
	ctx context.Context,
	st *state.State,
	trader *keypair.Full,
	tmpl soroswapSwapTemplate,
	templateIdx int,
) bool {
	if err := ctx.Err(); err != nil {
		s.addError(err)
		s.logProgress("soroswap benchmark footprint validation progress")
		return false
	}
	s.benchmarkProbes++
	invokeArgs, err := sharedsoroswap.RewriteInvokeContractAccount(tmpl.invokeArgs, tmpl.traderAddress, trader.Address())
	if err != nil {
		s.addError(fmt.Errorf("rewrite Soroswap benchmark invoke args for %s template=%d: %w", trader.Address(), templateIdx, err))
		s.logProgress("soroswap benchmark footprint validation progress")
		return true
	}
	if _, err := sharedsoroswap.RewriteSorobanAuthEntriesAccount(tmpl.simulation.AuthEntries, tmpl.traderAddress, trader.Address()); err != nil {
		s.addError(fmt.Errorf("rewrite Soroswap benchmark auth for %s template=%d: %w", trader.Address(), templateIdx, err))
		s.logProgress("soroswap benchmark footprint validation progress")
		return true
	}
	benchmarkFootprint, err := buildSoroswapFootprint(tmpl.simulation.Footprint, tmpl.traderAddress, trader.Address(), tmpl.inputAsset, tmpl.outputAsset)
	if err != nil {
		s.addError(fmt.Errorf("build Soroswap benchmark footprint for %s template=%d: %w", trader.Address(), templateIdx, err))
		s.logProgress("soroswap benchmark footprint validation progress")
		return true
	}
	_, _ = soroswapResourcesForFootprint(tmpl.simulation, benchmarkFootprint)

	result, err := restoreInvokeContract(ctx, st, trader, trader.Address(), invokeArgs, benchmarkBaseFeeMin, sharedsoroban.RestoreProbeOptions{
		DryRun:    true,
		PadFactor: resourcePadFactor,
	})
	if err != nil {
		s.addError(fmt.Errorf("simulate Soroswap benchmark validation probe for %s template=%d: %w", trader.Address(), templateIdx, err))
		s.logProgress("soroswap benchmark footprint validation progress")
		return ctx.Err() == nil
	}
	if result.RestoreNeeded {
		s.restoreNeededProbes++
		s.logProgress("soroswap benchmark footprint validation progress")
		return true
	}
	if !result.HasSimulation {
		s.addError(fmt.Errorf("simulate Soroswap benchmark validation probe for %s template=%d returned no simulation", trader.Address(), templateIdx))
		s.logProgress("soroswap benchmark footprint validation progress")
		return true
	}

	comparison, err := compareSoroswapBenchmarkFootprints(result.Simulation.Footprint, benchmarkFootprint)
	if err != nil {
		s.addError(fmt.Errorf("compare Soroswap benchmark footprint for %s template=%d: %w", trader.Address(), templateIdx, err))
		s.logProgress("soroswap benchmark footprint validation progress")
		return true
	}
	s.missingReadOnlyKeys += comparison.missingReadOnlyKeys
	s.missingReadWriteKeys += comparison.missingReadWriteKeys
	s.unexpectedReadOnlyKeys += comparison.unexpectedReadOnlyKeys
	s.unexpectedReadWriteKeys += comparison.unexpectedReadWriteKeys
	s.allowedExtraReadWriteKeys += comparison.allowedExtraReadWriteKeys
	if comparison.hasMismatch() {
		s.footprintMismatches++
		s.addError(fmt.Errorf(
			"Soroswap benchmark footprint mismatch for %s template=%d: missingReadOnly=%d missingReadWrite=%d unexpectedReadOnly=%d unexpectedReadWrite=%d",
			trader.Address(),
			templateIdx,
			comparison.missingReadOnlyKeys,
			comparison.missingReadWriteKeys,
			comparison.unexpectedReadOnlyKeys,
			comparison.unexpectedReadWriteKeys,
		))
	}
	s.logProgress("soroswap benchmark footprint validation progress")
	if err := ctx.Err(); err != nil {
		s.addError(err)
		return false
	}
	return true
}

func compareSoroswapBenchmarkFootprints(simulated xdr.LedgerFootprint, benchmark xdr.LedgerFootprint) (soroswapFootprintComparison, error) {
	simulatedKeys, err := encodeFootprint(simulated)
	if err != nil {
		return soroswapFootprintComparison{}, fmt.Errorf("encode simulated footprint: %w", err)
	}
	benchmarkKeys, err := encodeFootprint(benchmark)
	if err != nil {
		return soroswapFootprintComparison{}, fmt.Errorf("encode benchmark footprint: %w", err)
	}

	var comparison soroswapFootprintComparison
	for encoded := range simulatedKeys.readOnly {
		if _, ok := benchmarkKeys.any[encoded]; !ok {
			comparison.missingReadOnlyKeys++
		}
	}
	for encoded := range simulatedKeys.readWrite {
		if _, ok := benchmarkKeys.readWrite[encoded]; !ok {
			comparison.missingReadWriteKeys++
		}
	}
	for encoded := range benchmarkKeys.readOnly {
		if _, ok := simulatedKeys.any[encoded]; !ok {
			comparison.unexpectedReadOnlyKeys++
		}
	}
	for encoded, key := range benchmarkKeys.readWrite {
		if _, ok := simulatedKeys.any[encoded]; ok {
			continue
		}
		if key.Type == xdr.LedgerEntryTypeTrustline {
			comparison.allowedExtraReadWriteKeys++
			continue
		}
		comparison.unexpectedReadWriteKeys++
	}
	return comparison, nil
}

func encodeFootprint(footprint xdr.LedgerFootprint) (encodedFootprint, error) {
	encoded := encodedFootprint{
		readOnly:  make(map[string]xdr.LedgerKey, len(footprint.ReadOnly)),
		readWrite: make(map[string]xdr.LedgerKey, len(footprint.ReadWrite)),
		any:       make(map[string]xdr.LedgerKey, len(footprint.ReadOnly)+len(footprint.ReadWrite)),
	}
	for _, key := range footprint.ReadOnly {
		keyID, err := xdr.MarshalBase64(key)
		if err != nil {
			return encodedFootprint{}, err
		}
		encoded.readOnly[keyID] = key
		encoded.any[keyID] = key
	}
	for _, key := range footprint.ReadWrite {
		keyID, err := xdr.MarshalBase64(key)
		if err != nil {
			return encodedFootprint{}, err
		}
		encoded.readWrite[keyID] = key
		encoded.any[keyID] = key
	}
	return encoded, nil
}

func (c soroswapFootprintComparison) hasMismatch() bool {
	return c.missingReadOnlyKeys > 0 ||
		c.missingReadWriteKeys > 0 ||
		c.unexpectedReadOnlyKeys > 0 ||
		c.unexpectedReadWriteKeys > 0
}

func (s *soroswapBenchmarkFootprintValidationSummary) addError(err error) {
	if err == nil {
		return
	}
	s.errorCount++
	if len(s.errors) >= restoreMaxLoggedErrors {
		return
	}
	s.errors = append(s.errors, err)
}

func (s soroswapBenchmarkFootprintValidationSummary) error() error {
	var errs []error
	if len(s.errors) > 0 {
		errs = append(errs, errors.Join(s.errors...))
	}
	if !s.dryRun && s.restoreNeededProbes > 0 {
		errs = append(errs, fmt.Errorf("%d Soroswap benchmark footprint validation probes still require restore", s.restoreNeededProbes))
	}
	if s.footprintMismatches > 0 && len(s.errors) == 0 {
		errs = append(errs, fmt.Errorf("%d Soroswap benchmark footprint validation probes mismatched fresh simulation", s.footprintMismatches))
	}
	return errors.Join(errs...)
}

func (s soroswapBenchmarkFootprintValidationSummary) logStart(message string) {
	if s.logger == nil {
		return
	}
	s.logger.WithFields(log.F{
		"mode":                 config.ModeSoroswap,
		"dryRun":               s.dryRun,
		"accountStart":         s.accountStart,
		"accountEnd":           s.accountEnd,
		"selectedAccounts":     s.selectedAccounts,
		"totalBenchmarkProbes": s.totalBenchmarkProbes,
		"progressInterval":     s.progressInterval,
	}).Info(message)
}

func (s soroswapBenchmarkFootprintValidationSummary) logProgress(message string) {
	if s.logger == nil || s.progressInterval < 0 {
		return
	}
	if s.benchmarkProbes != 1 && s.benchmarkProbes != s.totalBenchmarkProbes && (s.progressInterval == 0 || s.benchmarkProbes%s.progressInterval != 0) {
		return
	}
	s.logger.WithFields(log.F{
		"mode":                      config.ModeSoroswap,
		"dryRun":                    s.dryRun,
		"templateProbes":            s.templateProbes,
		"templates":                 s.templates,
		"benchmarkProbes":           s.benchmarkProbes,
		"totalBenchmarkProbes":      s.totalBenchmarkProbes,
		"restoreNeededProbes":       s.restoreNeededProbes,
		"footprintMismatches":       s.footprintMismatches,
		"missingReadOnlyKeys":       s.missingReadOnlyKeys,
		"missingReadWriteKeys":      s.missingReadWriteKeys,
		"unexpectedReadOnlyKeys":    s.unexpectedReadOnlyKeys,
		"unexpectedReadWriteKeys":   s.unexpectedReadWriteKeys,
		"allowedExtraReadWriteKeys": s.allowedExtraReadWriteKeys,
		"skippedBenchmarkProbes":    s.skippedBenchmarkProbes,
		"errors":                    s.errorCount,
		"elapsed":                   time.Since(s.startedAt).String(),
	}).Info(message)
}

func (s *restoreSummary) recordSoroswapProbe(ctx context.Context, st *state.State, routerID xdr.ContractId, trader *keypair.Full, probe soroswapRestoreProbe, deadline uint64) bool {
	invokeArgs, err := sharedsoroswap.BuildSwapInvokeArgs(routerID, trader.Address(), probe.inputToken, probe.outputToken, probe.amountIn, deadline)
	if err != nil {
		s.addError(fmt.Errorf("build Soroswap restore probe for %s: %w", trader.Address(), err))
		return true
	}
	return s.recordProbe(ctx, st, trader, trader.Address(), invokeArgs)
}

func (s *restoreSummary) recordProbe(ctx context.Context, st *state.State, txSourceKP *keypair.Full, opSourceAddress string, invokeArgs xdr.InvokeContractArgs) bool {
	if err := ctx.Err(); err != nil {
		s.addError(err)
		s.logProgress(s.probes, "restore progress")
		return false
	}
	s.probes++
	probeNumber := s.probes
	result, err := restoreInvokeContract(ctx, st, txSourceKP, opSourceAddress, invokeArgs, benchmarkBaseFeeMin, sharedsoroban.RestoreProbeOptions{
		DryRun:    s.dryRun,
		PadFactor: resourcePadFactor,
	})
	if err != nil {
		s.addError(err)
		s.logProgress(probeNumber, "restore progress")
		return ctx.Err() == nil
	}
	if result.RestoreNeeded {
		s.restoreNeededProbes++
	} else {
		s.noopProbes++
	}
	if result.AutoRestoreKeys > 0 {
		s.autoRestoreProbes++
		s.autoRestoreKeys += result.AutoRestoreKeys
	}
	s.restoreTransactions += result.RestoreTransactions
	s.readOnlyKeys += result.ReadOnlyKeys
	s.readWriteKeys += result.ReadWriteKeys
	s.resourceFee += result.ResourceFee
	s.logProgress(probeNumber, "restore progress")
	if err := ctx.Err(); err != nil {
		s.addError(err)
		return false
	}
	return true
}

func normalizeRestoreProgressInterval(interval int) int {
	if interval == 0 {
		return defaultRestoreProgressInterval
	}
	return interval
}

func selectAccountRange(accounts []*keypair.Full, start, limit int) ([]*keypair.Full, int, int, error) {
	if start < 0 {
		return nil, 0, 0, fmt.Errorf("account-start must be >= 0")
	}
	if limit < 0 {
		return nil, 0, 0, fmt.Errorf("account-limit must be >= 0")
	}
	if start >= len(accounts) {
		return nil, start, start, fmt.Errorf("account-start %d is outside account pool of size %d", start, len(accounts))
	}
	end := len(accounts)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return accounts[start:end], start, end, nil
}

func firstDifferentAccount(accounts []*keypair.Full, address string) *keypair.Full {
	for _, kp := range accounts {
		if kp != nil && kp.Address() != address {
			return kp
		}
	}
	return nil
}

func (s *restoreSummary) addError(err error) {
	if err == nil {
		return
	}
	s.errors = append(s.errors, err)
}

func (s restoreSummary) error() error {
	return errors.Join(s.errors...)
}

func (s restoreSummary) logStart(message string) {
	if s.logger == nil {
		return
	}
	s.logger.WithFields(log.F{
		"mode":             s.mode,
		"dryRun":           s.dryRun,
		"accountStart":     s.accountStart,
		"accountEnd":       s.accountEnd,
		"selectedAccounts": s.selectedAccounts,
		"totalProbes":      s.totalProbes,
		"progressInterval": s.progressInterval,
	}).Info(message)
}

func (s restoreSummary) logProgress(probeNumber int, message string) {
	if s.logger == nil || s.progressInterval < 0 {
		return
	}
	if probeNumber != 1 && probeNumber != s.totalProbes && (s.progressInterval == 0 || probeNumber%s.progressInterval != 0) {
		return
	}
	s.logger.WithFields(log.F{
		"mode":                s.mode,
		"dryRun":              s.dryRun,
		"probes":              s.probes,
		"totalProbes":         s.totalProbes,
		"restoreNeededProbes": s.restoreNeededProbes,
		"restoreTransactions": s.restoreTransactions,
		"autoRestoreProbes":   s.autoRestoreProbes,
		"autoRestoreKeys":     s.autoRestoreKeys,
		"noopProbes":          s.noopProbes,
		"readOnlyKeys":        s.readOnlyKeys,
		"readWriteKeys":       s.readWriteKeys,
		"errors":              len(s.errors),
		"elapsed":             time.Since(s.startedAt).String(),
	}).Info(message)
}

func logRestoreSummary(logger *log.Entry, summary restoreSummary) {
	if logger == nil {
		return
	}
	fields := log.F{
		"mode":                summary.mode,
		"dryRun":              summary.dryRun,
		"probes":              summary.probes,
		"restoreNeededProbes": summary.restoreNeededProbes,
		"restoreTransactions": summary.restoreTransactions,
		"autoRestoreProbes":   summary.autoRestoreProbes,
		"autoRestoreKeys":     summary.autoRestoreKeys,
		"noopProbes":          summary.noopProbes,
		"readOnlyKeys":        summary.readOnlyKeys,
		"readWriteKeys":       summary.readWriteKeys,
		"resourceFee":         summary.resourceFee,
		"totalProbes":         summary.totalProbes,
		"accountStart":        summary.accountStart,
		"accountEnd":          summary.accountEnd,
		"selectedAccounts":    summary.selectedAccounts,
		"elapsed":             time.Since(summary.startedAt).String(),
		"errors":              len(summary.errors),
	}
	entry := logger.WithFields(fields)
	for i, err := range summary.errors {
		if i >= restoreMaxLoggedErrors {
			break
		}
		entry = entry.WithField(fmt.Sprintf("error%d", i+1), err.Error())
	}
	if summary.dryRun {
		entry.Info("restore dry-run summary: would restore archived state where needed")
		return
	}
	entry.Info("restore summary")
}

func logSoroswapBenchmarkFootprintValidationSummary(logger *log.Entry, summary soroswapBenchmarkFootprintValidationSummary) {
	if logger == nil {
		return
	}
	fields := log.F{
		"mode":                      config.ModeSoroswap,
		"dryRun":                    summary.dryRun,
		"templateProbes":            summary.templateProbes,
		"templates":                 summary.templates,
		"benchmarkProbes":           summary.benchmarkProbes,
		"totalBenchmarkProbes":      summary.totalBenchmarkProbes,
		"restoreNeededProbes":       summary.restoreNeededProbes,
		"footprintMismatches":       summary.footprintMismatches,
		"missingReadOnlyKeys":       summary.missingReadOnlyKeys,
		"missingReadWriteKeys":      summary.missingReadWriteKeys,
		"unexpectedReadOnlyKeys":    summary.unexpectedReadOnlyKeys,
		"unexpectedReadWriteKeys":   summary.unexpectedReadWriteKeys,
		"allowedExtraReadWriteKeys": summary.allowedExtraReadWriteKeys,
		"skippedBenchmarkProbes":    summary.skippedBenchmarkProbes,
		"accountStart":              summary.accountStart,
		"accountEnd":                summary.accountEnd,
		"selectedAccounts":          summary.selectedAccounts,
		"elapsed":                   time.Since(summary.startedAt).String(),
		"errors":                    summary.errorCount,
	}
	entry := logger.WithFields(fields)
	for i, err := range summary.errors {
		if i >= restoreMaxLoggedErrors {
			break
		}
		entry = entry.WithField(fmt.Sprintf("error%d", i+1), err.Error())
	}
	entry.Info("soroswap benchmark footprint validation summary")
}

func logRestoreVerifySummary(logger *log.Entry, summary restoreSummary) {
	if logger == nil {
		return
	}
	logger.WithFields(log.F{
		"mode":                summary.mode,
		"probes":              summary.probes,
		"restoreNeededProbes": summary.restoreNeededProbes,
		"autoRestoreProbes":   summary.autoRestoreProbes,
		"autoRestoreKeys":     summary.autoRestoreKeys,
		"noopProbes":          summary.noopProbes,
		"errors":              len(summary.errors),
	}).Info("restore verification summary")
}
