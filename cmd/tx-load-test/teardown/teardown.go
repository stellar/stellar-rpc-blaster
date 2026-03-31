// Package teardown merges participant accounts back into the fee payer,
// recovering reserved XLM.
package teardown

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/stellar/go-stellar-sdk/amount"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

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

	if st == nil || st.RPCClient == nil || st.FeePayerKP == nil {
		logger.Warn("state not initialized, nothing to do")
		return nil
	}

	// Filter to accounts that still exist on-chain.
	var toMerge []*keypair.Full
	for _, kp := range st.AccountKPs {
		if kp == nil {
			continue
		}
		exists, err := state.AccountExists(ctx, st.RPCClient, kp.Address())
		if err != nil {
			logger.WithError(err).Warnf("check %s", kp.Address())
			continue
		}
		if exists {
			toMerge = append(toMerge, kp)
		}
	}

	if len(toMerge) == 0 {
		logger.Info("no child accounts to merge")
		if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
			logger.WithError(err).Warn("could not delete state file")
		} else {
			logger.Infof("deleted %s", stateFile)
		}
		return nil
	}

	logger.Infof("merging %d accounts back to fee payer", len(toMerge))

	n := len(toMerge)
	batches := (n + mergeBatchSize - 1) / mergeBatchSize
	for b := range batches {
		start := b * mergeBatchSize
		end := min(start+mergeBatchSize, n)
		batch := toMerge[start:end]

		if err := cleanupBatch(ctx, logger, cfg, st, b+1, batches, batch); err != nil {
			logger.WithError(err).Warnf("batch %d/%d: skipping", b+1, batches)
			continue
		}

		// Remove merged accounts from state and save after each successful batch
		// so progress is durable across crashes and interrupts.
		mergedSet := make(map[string]struct{}, len(batch))
		for _, kp := range batch {
			mergedSet[kp.Address()] = struct{}{}
		}
		var remaining []*keypair.Full
		for _, kp := range st.AccountKPs {
			if _, ok := mergedSet[kp.Address()]; !ok {
				remaining = append(remaining, kp)
			}
		}
		st.AccountKPs = remaining
		if len(st.SACHolderKPs) > 0 {
			var remainingHolders []*keypair.Full
			for _, kp := range st.SACHolderKPs {
				if _, ok := mergedSet[kp.Address()]; !ok {
					remainingHolders = append(remainingHolders, kp)
				}
			}
			st.SACHolderKPs = remainingHolders
		}
		ps, err := st.ToPersistedState(cfg.RPCURL)
		if err != nil {
			return fmt.Errorf("build state after batch %d/%d: %w", b+1, batches, err)
		}
		if err := ps.Save(stateFile); err != nil {
			return fmt.Errorf("save state after batch %d/%d: %w", b+1, batches, err)
		}
	}

	if len(st.AccountKPs) > 0 {
		logger.Warnf("%d accounts could not be merged -- re-run teardown", len(st.AccountKPs))
		return fmt.Errorf("teardown incomplete: %d accounts remain", len(st.AccountKPs))
	}

	// Full success -- delete the state file.
	logger.Info("all accounts merged")
	if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
		logger.WithError(err).Warn("could not delete state file")
	} else {
		logger.Infof("deleted %s", stateFile)
	}
	return nil
}

// BestEffortCleanup is called on interrupt / panic during setup. It attempts
// to merge whatever accounts exist, writes partial state to the file, and
// suggests re-running teardown.
func BestEffortCleanup(logger *log.Entry, cfg config.Config, st *state.State, stateFile string) {
	logger = logger.WithField("phase", "cleanup")

	if st == nil || st.RPCClient == nil || st.FeePayerKP == nil {
		logger.Warn("state not initialized, nothing to clean up")
		return
	}

	// Cap total cleanup time.
	ctx, cancel := context.WithTimeout(context.Background(), maxCleanupTimeout)
	defer cancel()

	// Write state first so teardown can pick up if cleanup fails.
	ps, err := st.ToPersistedState(cfg.RPCURL)
	if err != nil {
		logger.WithError(err).Error("failed to build state file before cleanup")
		return
	}
	if err := ps.Save(stateFile); err != nil {
		logger.WithError(err).Error("failed to write state file before cleanup")
		return
	} else {
		logger.Infof("wrote partial state to %s", stateFile)
	}

	var toMerge []*keypair.Full
	for _, kp := range st.AccountKPs {
		if kp == nil {
			continue
		}
		exists, err := state.AccountExists(ctx, st.RPCClient, kp.Address())
		if err != nil {
			continue
		}
		if exists {
			toMerge = append(toMerge, kp)
		}
	}

	if len(toMerge) == 0 {
		logger.Info("no child accounts found on-chain")
		return
	}

	logger.Infof("best-effort merge of %d accounts", len(toMerge))
	n := len(toMerge)
	batches := (n + mergeBatchSize - 1) / mergeBatchSize
	for b := range batches {
		start := b * mergeBatchSize
		end := min(start+mergeBatchSize, n)
		batch := toMerge[start:end]

		if err := cleanupBatch(ctx, logger, cfg, st, b+1, batches, batch); err != nil {
			logger.WithError(err).Warnf("batch %d/%d: skipping", b+1, batches)
			continue
		}

		// Remove merged accounts and save incrementally.
		mergedSet := make(map[string]struct{}, len(batch))
		for _, kp := range batch {
			mergedSet[kp.Address()] = struct{}{}
		}
		var remaining []*keypair.Full
		for _, kp := range st.AccountKPs {
			if _, ok := mergedSet[kp.Address()]; !ok {
				remaining = append(remaining, kp)
			}
		}
		st.AccountKPs = remaining
		if len(st.SACHolderKPs) > 0 {
			var remainingHolders []*keypair.Full
			for _, kp := range st.SACHolderKPs {
				if _, ok := mergedSet[kp.Address()]; !ok {
					remainingHolders = append(remainingHolders, kp)
				}
			}
			st.SACHolderKPs = remainingHolders
		}
		ps, err = st.ToPersistedState(cfg.RPCURL)
		if err != nil {
			logger.WithError(err).Errorf("build state after batch %d/%d", b+1, batches)
			return
		}
		if err := ps.Save(stateFile); err != nil {
			logger.WithError(err).Errorf("save state after batch %d/%d", b+1, batches)
			return
		}
	}

	if len(st.AccountKPs) > 0 {
		logger.Warnf("%d accounts remain -- run 'teardown' to finish cleanup", len(st.AccountKPs))
	} else {
		logger.Info("all accounts merged during best-effort cleanup")
		if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
			logger.WithError(err).Warn("could not delete state file")
		}
	}
}

// cleanupBatch drains, de-trusts, and merges a single batch of accounts in
// two sequential fee-bumped transactions:
//
//  1. Drain: one Payment per non-zero trustline balance back to the fee payer.
//     Confirmed before pass 2 to avoid ChangeTrustInvalidLimit caused by
//     in-flight SAC transfers that landed after the balance was read.
//  2. Remove + merge: one ChangeTrust(limit=0) per asset, then AccountMerge.
func cleanupBatch(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	batchNum, totalBatches int,
	batch []*keypair.Full,
) error {
	// Fetch trustline balances upfront.
	tlBalances, err := fetchTrustlineBalances(ctx, st, batch)
	if err != nil {
		return fmt.Errorf("fetch trustline balances: %w", err)
	}

	// --- pass 1: drain non-zero balances ---------------------------------
	var drainOps []txnbuild.Operation
	drainSignerSet := make(map[string]*keypair.Full, len(batch))
	drainSignerSet[batch[0].Address()] = batch[0]
	for j, kp := range batch {
		srcAccount := ""
		if j > 0 {
			srcAccount = kp.Address()
		}
		for _, asset := range st.Assets {
			bal, hasTrustline := tlBalances[kp.Address()][asset.GetCode()]
			if !hasTrustline || bal == 0 {
				continue
			}
			drainSignerSet[kp.Address()] = kp
			drainOps = append(drainOps, &txnbuild.Payment{
				Destination:   st.FeePayerKP.Address(),
				Asset:         asset,
				Amount:        amount.String(bal),
				SourceAccount: srcAccount,
			})
		}
	}
	if len(drainOps) > 0 {
		src, err := st.RPCClient.LoadAccount(ctx, batch[0].Address())
		if err != nil {
			return fmt.Errorf("load account for drain: %w", err)
		}
		drainTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        src,
			IncrementSequenceNum: true,
			Operations:           drainOps,
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(state.TxTimeBoundSecs)},
		})
		if err != nil {
			return fmt.Errorf("build drain tx: %w", err)
		}
		drainSigners := make([]*keypair.Full, 0, len(drainSignerSet))
		for _, kp := range batch {
			if signer, ok := drainSignerSet[kp.Address()]; ok {
				drainSigners = append(drainSigners, signer)
			}
		}
		drainTx, err = drainTx.Sign(cfg.NetworkPassphrase, drainSigners...)
		if err != nil {
			return fmt.Errorf("sign drain tx: %w", err)
		}
		if err := state.SubmitFeeBumpAndWait(ctx, logger, st.RPCClient, cfg.NetworkPassphrase, st.FeePayerKP, drainTx); err != nil {
			return fmt.Errorf("submit drain tx: %w", err)
		}
	}

	// --- pass 2: remove trustlines + merge -------------------------------
	mergeOps := make([]txnbuild.Operation, 0, len(batch)*4)
	for j, kp := range batch {
		srcAccount := ""
		if j > 0 {
			srcAccount = kp.Address()
		}
		for _, asset := range st.Assets {
			_, hasTrustline := tlBalances[kp.Address()][asset.GetCode()]
			if !hasTrustline {
				continue
			}
			mergeOps = append(mergeOps, &txnbuild.ChangeTrust{
				Line:          txnbuild.ChangeTrustAssetWrapper{Asset: asset},
				Limit:         "0",
				SourceAccount: srcAccount,
			})
		}
		mergeOps = append(mergeOps, &txnbuild.AccountMerge{
			Destination:   st.FeePayerKP.Address(),
			SourceAccount: srcAccount,
		})
	}

	src, err := st.RPCClient.LoadAccount(ctx, batch[0].Address())
	if err != nil {
		return fmt.Errorf("load account for merge: %w", err)
	}
	mergeTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        src,
		IncrementSequenceNum: true,
		Operations:           mergeOps,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(state.TxTimeBoundSecs)},
	})
	if err != nil {
		return fmt.Errorf("build merge tx: %w", err)
	}
	mergeTx, err = mergeTx.Sign(cfg.NetworkPassphrase, batch...)
	if err != nil {
		return fmt.Errorf("sign merge tx: %w", err)
	}
	if err := state.SubmitFeeBumpAndWait(ctx, logger, st.RPCClient, cfg.NetworkPassphrase, st.FeePayerKP, mergeTx); err != nil {
		return fmt.Errorf("submit merge tx: %w", err)
	}

	logger.Infof("batch %d/%d merged (%d accounts)", batchNum, totalBatches, len(batch))
	return nil
}

// fetchTrustlineBalances returns a nested map of accountAddress  ->  assetCode  ->
// balance-in-stroops for every trustline that exists on-ledger for the given
// batch of accounts. Trustlines not present on-ledger are simply absent from
// the map. A single GetLedgerEntries call covers the whole batch.
func fetchTrustlineBalances(
	ctx context.Context,
	st *state.State,
	batch []*keypair.Full,
) (map[string]map[string]xdr.Int64, error) {
	type keyMeta struct{ account, assetCode string }

	keys := make([]string, 0, len(batch)*len(st.Assets))
	metas := make([]keyMeta, 0, len(batch)*len(st.Assets))

	for _, kp := range batch {
		accountID, err := xdr.AddressToAccountId(kp.Address())
		if err != nil {
			return nil, fmt.Errorf("parse account %s: %w", kp.Address(), err)
		}
		for _, asset := range st.Assets {
			ax, err := asset.ToXDR()
			if err != nil {
				return nil, fmt.Errorf("asset %s to XDR: %w", asset.GetCode(), err)
			}
			lk := xdr.LedgerKey{
				Type: xdr.LedgerEntryTypeTrustline,
				TrustLine: &xdr.LedgerKeyTrustLine{
					AccountId: accountID,
					Asset: xdr.TrustLineAsset{
						Type:       ax.Type,
						AlphaNum4:  ax.AlphaNum4,
						AlphaNum12: ax.AlphaNum12,
					},
				},
			}
			b64, err := xdr.MarshalBase64(lk)
			if err != nil {
				return nil, fmt.Errorf("marshal trustline key: %w", err)
			}
			keys = append(keys, b64)
			metas = append(metas, keyMeta{kp.Address(), asset.GetCode()})
		}
	}

	resp, err := st.RPCClient.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys})
	if err != nil {
		return nil, fmt.Errorf("get ledger entries: %w", err)
	}

	// Build a lookup from request key  ->  meta so we can match response entries
	// (the server only returns entries that exist; they include KeyXDR for
	// matching back to the original request key).
	keyToMeta := make(map[string]keyMeta, len(keys))
	for i, k := range keys {
		keyToMeta[k] = metas[i]
	}

	result := make(map[string]map[string]xdr.Int64)
	for _, entry := range resp.Entries {
		meta, ok := keyToMeta[entry.KeyXDR]
		if !ok {
			continue
		}
		var data xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(entry.DataXDR, &data); err != nil {
			continue
		}
		tl := data.TrustLine
		if tl == nil {
			continue
		}
		if result[meta.account] == nil {
			result[meta.account] = make(map[string]xdr.Int64)
		}
		result[meta.account][meta.assetCode] = tl.Balance
	}
	return result, nil
}
