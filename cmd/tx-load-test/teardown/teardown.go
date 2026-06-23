// Package teardown merges participant accounts back into the fee payer,
// recovering reserved XLM.
package teardown

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// mergeBatchSize is the number of accounts per cleanup batch. Cleanup now uses
// two sequential transactions per batch:
//   - Drain tx:  up to 3 Payment ops per account  (12 * 3 = 36 ops max)
//   - Merge tx:  up to 3 ChangeTrust + 1 AccountMerge per account (12 * 4 = 48 ops max)
//
// Both are well under the 100-op limit.
const mergeBatchSize = 12

// maxCleanupTimeout caps the total time allowed for teardown regardless of
// account count.
const maxCleanupTimeout = 10 * time.Minute

// Teardown merges participant accounts back into the fee-payer account,
// recovering reserved XLM.
//
// When keep <= 0 it is a full teardown: every participant account is merged
// and the state file is deleted on full success; on partial failure the file
// is updated with the remaining accounts.
//
// When keep > 0 it is a partial teardown: only the tail of the pool
// (AccountKPs[keep:]) is merged, the first keep accounts are retained, and the
// state file is always kept and updated. force allows merging into the
// benchmark holder subset (which also shrinks it); without it, a keep that
// would merge holder accounts is refused.
func Teardown(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State, stateFile string, keep int, force bool) error {
	logger = logger.WithField("phase", "teardown")

	if !cleanupStateReady(logger, st) {
		return nil
	}

	if keep > 0 {
		return partialTeardown(ctx, logger, cfg, st, stateFile, keep, force)
	}

	toMerge := existingCleanupAccounts(ctx, logger, st, true)
	if len(toMerge) == 0 {
		logger.Info("no child accounts to merge")
		deleteStateFile(logger, stateFile)
		return nil
	}

	logger.Infof("merging %d accounts back to fee payer", len(toMerge))
	if err := runCleanupBatches(ctx, logger, cfg, st, stateFile, toMerge); err != nil {
		return err
	}

	return finalizeTeardown(logger, st, stateFile)
}

// partialTeardown merges the tail of the pool (everything after the first
// keep accounts) back into the fee payer, retaining the front and always
// keeping the state file.
func partialTeardown(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State, stateFile string, keep int, force bool) error {
	pool := len(st.AccountKPs)
	if keep >= pool {
		logger.Infof("pool has %d accounts, at or below keep=%d -- nothing to merge", pool, keep)
		return nil
	}

	if safe := minSafeKeep(st); keep < safe && !force {
		return fmt.Errorf(
			"keep=%d would merge benchmark holder accounts -- keep at least %d to preserve the holder subset, or pass --force to merge into it (shrinks the holder set)",
			keep, safe)
	}

	tail := st.AccountKPs[keep:]

	// Tail accounts that no longer exist on-chain are already gone; drop them
	// from state so they don't count against the keep target or trigger a false
	// "incomplete" result. Persist that pruning before merging.
	toMerge := filterExistingAccounts(ctx, logger, st, tail, true)
	if gone := accountsNotIn(tail, toMerge); len(gone) > 0 {
		removeMergedAccounts(st, gone)
		if err := saveStateSnapshot(cfg, st, stateFile); err != nil {
			return fmt.Errorf("save state after pruning %d absent tail accounts: %w", len(gone), err)
		}
		logger.Infof("pruned %d tail accounts already absent on-chain", len(gone))
	}

	if len(toMerge) == 0 {
		logger.Infof("no tail accounts to merge; pool now %d accounts (kept first %d)", len(st.AccountKPs), keep)
		return nil
	}

	logger.Infof("partial teardown: merging %d tail accounts, keeping first %d", len(toMerge), keep)
	if err := runCleanupBatches(ctx, logger, cfg, st, stateFile, toMerge); err != nil {
		return err
	}

	return finalizePartialTeardown(logger, cfg, st, stateFile, keep)
}

// BestEffortCleanup is called on interrupt / panic during setup. It attempts
// to merge whatever accounts exist, writes partial state to the file, and
// suggests re-running teardown.
func BestEffortCleanup(logger *log.Entry, cfg config.Config, st *state.State, stateFile string) {
	logger = logger.WithField("phase", "cleanup")

	if !cleanupStateReady(logger, st) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), maxCleanupTimeout)
	defer cancel()

	if err := saveStateSnapshot(cfg, st, stateFile); err != nil {
		logger.WithError(err).Error("failed to write state file before cleanup")
		return
	}
	logger.Infof("wrote partial state to %s", stateFile)

	toMerge := existingCleanupAccounts(ctx, logger, st, false)
	if len(toMerge) == 0 {
		logger.Info("no child accounts found on-chain")
		return
	}

	logger.Infof("best-effort merge of %d accounts", len(toMerge))
	if err := runCleanupBatches(ctx, logger, cfg, st, stateFile, toMerge); err != nil {
		logger.WithError(err).Error("failed to persist cleanup progress")
		return
	}

	finalizeBestEffortCleanup(logger, st, stateFile)
}

func cleanupStateReady(logger *log.Entry, st *state.State) bool {
	if st == nil || st.RPCClient == nil || st.FeePayerKP == nil {
		logger.Warn("state not initialized, nothing to do")
		return false
	}
	return true
}

func finalizeTeardown(logger *log.Entry, st *state.State, stateFile string) error {
	if len(st.AccountKPs) > 0 {
		logger.Warnf("%d accounts could not be merged -- re-run teardown", len(st.AccountKPs))
		return fmt.Errorf("teardown incomplete: %d accounts remain", len(st.AccountKPs))
	}
	logger.Info("all accounts merged")
	deleteStateFile(logger, stateFile)
	return nil
}

func finalizePartialTeardown(logger *log.Entry, cfg config.Config, st *state.State, stateFile string, keep int) error {
	// runCleanupBatches snapshots after each batch; snapshot once more so the
	// final state is durable even if the last batch removed nothing.
	if err := saveStateSnapshot(cfg, st, stateFile); err != nil {
		return fmt.Errorf("save final state: %w", err)
	}

	remaining := len(st.AccountKPs)
	if remaining > keep {
		logger.Warnf("%d accounts remain but target was %d -- %d tail accounts could not be merged; re-run with the same --keep to finish",
			remaining, keep, remaining-keep)
		return fmt.Errorf("partial teardown incomplete: %d accounts remain, target %d", remaining, keep)
	}

	logger.Infof("partial teardown complete: pool now %d accounts (kept first %d); state file %s retained", remaining, keep, stateFile)
	if holders := len(st.SACHolderKPs); holders > 0 && remaining < holders {
		logger.Warnf("remaining pool %d is below the holder count %d -- benchmark capability may be reduced", remaining, holders)
	}
	return nil
}

// accountsNotIn returns the members of all that are absent from subset,
// compared by address.
func accountsNotIn(all, subset []*keypair.Full) []*keypair.Full {
	present := make(map[string]struct{}, len(subset))
	for _, kp := range subset {
		if kp != nil {
			present[kp.Address()] = struct{}{}
		}
	}
	var missing []*keypair.Full
	for _, kp := range all {
		if kp == nil {
			continue
		}
		if _, ok := present[kp.Address()]; !ok {
			missing = append(missing, kp)
		}
	}
	return missing
}

func finalizeBestEffortCleanup(logger *log.Entry, st *state.State, stateFile string) {
	if len(st.AccountKPs) > 0 {
		logger.Warnf("%d accounts remain -- run 'teardown' to finish cleanup", len(st.AccountKPs))
		return
	}
	logger.Info("all accounts merged during best-effort cleanup")
	deleteStateFile(logger, stateFile)
}
