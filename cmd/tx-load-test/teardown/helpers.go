package teardown

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func existingCleanupAccounts(ctx context.Context, logger *log.Entry, st *state.State, warnOnLookupError bool) []*keypair.Full {
	return filterExistingAccounts(ctx, logger, st, st.AccountKPs, warnOnLookupError)
}

// filterExistingAccounts returns the subset of kps that currently exist
// on-chain. It is used both for the full pool and for a tail slice in a
// partial teardown.
func filterExistingAccounts(ctx context.Context, logger *log.Entry, st *state.State, kps []*keypair.Full, warnOnLookupError bool) []*keypair.Full {
	var toMerge []*keypair.Full
	for _, kp := range kps {
		if kp == nil {
			continue
		}
		exists, err := state.AccountExists(ctx, st.RPCClient, kp.Address())
		if err != nil {
			if warnOnLookupError {
				logger.WithError(err).Warnf("check %s", kp.Address())
			}
			continue
		}
		if exists {
			toMerge = append(toMerge, kp)
		}
	}
	return toMerge
}

// minSafeKeep returns the smallest --keep value that does not merge any
// benchmark holder account. Holders are the trustlined/token-funded subset
// (in practice a prefix of the pool); a keep below this would shrink the
// holder set and break benchmark modes. Membership-based so it is correct
// regardless of ordering.
func minSafeKeep(st *state.State) int {
	if len(st.SACHolderKPs) == 0 {
		return 0
	}
	holders := make(map[string]struct{}, len(st.SACHolderKPs))
	for _, kp := range st.SACHolderKPs {
		if kp != nil {
			holders[kp.Address()] = struct{}{}
		}
	}
	safe := 0
	for i, kp := range st.AccountKPs {
		if kp == nil {
			continue
		}
		if _, ok := holders[kp.Address()]; ok {
			safe = i + 1 // must retain through this holder's position
		}
	}
	return safe
}

func cleanupBatches(accountKPs []*keypair.Full, batchSize int) [][]*keypair.Full {
	if len(accountKPs) == 0 || batchSize <= 0 {
		return nil
	}
	batches := make([][]*keypair.Full, 0, (len(accountKPs)+batchSize-1)/batchSize)
	for start := 0; start < len(accountKPs); start += batchSize {
		end := min(start+batchSize, len(accountKPs))
		batches = append(batches, accountKPs[start:end])
	}
	return batches
}

func runCleanupBatches(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State, stateFile string, toMerge []*keypair.Full) error {
	batches := cleanupBatches(toMerge, mergeBatchSize)
	logger.Infof("cleanup will run %d batches", len(batches))
	for i, batch := range batches {
		batchStarted := time.Now()
		logger.Infof("batch %d/%d starting (%d accounts)", i+1, len(batches), len(batch))
		if err := cleanupBatch(ctx, logger, cfg, st, i+1, len(batches), batch); err != nil {
			logger.WithError(err).Warnf("batch %d/%d: skipping", i+1, len(batches))
			continue
		}

		removeMergedAccounts(st, batch)
		if err := saveStateSnapshot(cfg, st, stateFile); err != nil {
			return fmt.Errorf("save state after batch %d/%d: %w", i+1, len(batches), err)
		}
		logger.Infof("batch %d/%d snapshot saved (%d accounts remain, duration=%s)", i+1, len(batches), len(st.AccountKPs), time.Since(batchStarted).Round(time.Millisecond))
	}
	return nil
}

func saveStateSnapshot(cfg config.Config, st *state.State, stateFile string) error {
	ps, err := st.ToPersistedState(cfg.RPCURL)
	if err != nil {
		return fmt.Errorf("build state: %w", err)
	}
	if err := ps.Save(stateFile); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}

func removeMergedAccounts(st *state.State, merged []*keypair.Full) {
	mergedSet := make(map[string]struct{}, len(merged))
	for _, kp := range merged {
		if kp != nil {
			mergedSet[kp.Address()] = struct{}{}
		}
	}

	var remaining []*keypair.Full
	for _, kp := range st.AccountKPs {
		if kp == nil {
			continue
		}
		if _, ok := mergedSet[kp.Address()]; !ok {
			remaining = append(remaining, kp)
		}
	}
	st.AccountKPs = remaining

	if len(st.SACHolderKPs) == 0 {
		return
	}
	var remainingHolders []*keypair.Full
	for _, kp := range st.SACHolderKPs {
		if kp == nil {
			continue
		}
		if _, ok := mergedSet[kp.Address()]; !ok {
			remainingHolders = append(remainingHolders, kp)
		}
	}
	st.SACHolderKPs = remainingHolders
}

func deleteStateFile(logger *log.Entry, stateFile string) {
	if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
		logger.WithError(err).Warn("could not delete state file")
		return
	}
	logger.Infof("deleted %s", stateFile)
}
