// Package teardown merges participant accounts back into the fee payer,
// recovering reserved XLM.
package teardown

import (
	"context"
	"fmt"
	"time"

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

// Teardown merges every participant account back into the fee-payer account,
// recovering all reserved XLM. On full success it deletes the state file.
// On partial success it updates the state file to reflect remaining accounts
// and logs a suggestion to re-run teardown.
func Teardown(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State, stateFile string) error {
	logger = logger.WithField("phase", "teardown")

	if !cleanupStateReady(logger, st) {
		return nil
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

func finalizeBestEffortCleanup(logger *log.Entry, st *state.State, stateFile string) {
	if len(st.AccountKPs) > 0 {
		logger.Warnf("%d accounts remain -- run 'teardown' to finish cleanup", len(st.AccountKPs))
		return
	}
	logger.Info("all accounts merged during best-effort cleanup")
	deleteStateFile(logger, stateFile)
}
