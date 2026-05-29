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
	totalProbes         int
	progressInterval    int
	logger              *log.Entry
	startedAt           time.Time
	errors              []error
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
		if err != nil {
			errs = append(errs, fmt.Errorf("mode=%s: %w", mode, err))
		}
		if options.Verify && err == nil && !options.DryRun && ctx.Err() == nil {
			verifyOptions := options
			verifyOptions.DryRun = true
			verifySummary, verifyErr := restoreMode(ctx, logger, st, mode, verifyOptions)
			verifySummary.dryRun = false
			logRestoreVerifySummary(logger, verifySummary)
			if verifyErr != nil {
				errs = append(errs, fmt.Errorf("mode=%s verify: %w", mode, verifyErr))
			} else if verifySummary.restoreNeededProbes > 0 {
				errs = append(errs, fmt.Errorf("mode=%s verify: %d probes still require restore", mode, verifySummary.restoreNeededProbes))
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

func restoreSACContracts(ctx context.Context, st *state.State, _ RestoreOptions, summary restoreSummary) (restoreSummary, error) {
	if len(st.SACs) == 0 {
		summary.addError(fmt.Errorf("state has no SAC contracts"))
		return summary, summary.error()
	}
	summary.accountStart = 0
	summary.accountEnd = 0
	summary.selectedAccounts = 0
	summary.totalProbes = len(st.SACs)
	summary.logStart("restore mode started")
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
		invokeArgs := xdr.InvokeContractArgs{
			ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID},
			FunctionName:    "decimals",
		}
		if !summary.recordProbe(ctx, st, st.FeePayerKP, st.FeePayerKP.Address(), invokeArgs) {
			return summary, summary.error()
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
	holders := st.SACHolderKPs
	if len(holders) == 0 {
		holders = state.DefaultSACHolderKPs(st.AccountKPs)
	}
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

func logRestoreVerifySummary(logger *log.Entry, summary restoreSummary) {
	if logger == nil {
		return
	}
	logger.WithFields(log.F{
		"mode":                summary.mode,
		"probes":              summary.probes,
		"restoreNeededProbes": summary.restoreNeededProbes,
		"noopProbes":          summary.noopProbes,
		"errors":              len(summary.errors),
	}).Info("restore verification summary")
}
