package teardown

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/amount"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

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
	batchStarted := time.Now()
	tlBalances, err := ledger.FetchTrustlineBalances(ctx, st.RPCClient, st.Assets[:], batch, ledger.DefaultBatchSize)
	if err != nil {
		return fmt.Errorf("fetch trustline balances: %w", err)
	}

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
		logger.Infof("batch %d/%d drain pass (%d payments)", batchNum, totalBatches, len(drainOps))
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

	logger.Infof("batch %d/%d merged (%d accounts) in %s", batchNum, totalBatches, len(batch), time.Since(batchStarted).Round(time.Millisecond))
	return nil
}
