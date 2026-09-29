package setup

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// createSACHolderAccounts provisions the SAC-active participant subset via the
// fee payer. Each batch combines CreateAccount + ChangeTrust for new accounts
// (or trust-only for accounts that already exist from a previous partial run).
func createSACHolderAccounts(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	accountKPs []*keypair.Full,
	fundingAmount string,
) error {
	n := len(accountKPs)
	totalBatches := (n + createAndTrustBatchSize - 1) / createAndTrustBatchSize
	for b := range totalBatches {
		start := b * createAndTrustBatchSize
		end := min(start+createAndTrustBatchSize, n)
		batch := accountKPs[start:end]

		var newKPs, existingKPs []*keypair.Full
		for _, kp := range batch {
			exists, err := state.AccountExists(ctx, st.RPCClient, kp.Address())
			if err != nil {
				return fmt.Errorf("batch %d: check %s: %w", b+1, kp.Address(), err)
			}
			if exists {
				existingKPs = append(existingKPs, kp)
			} else {
				newKPs = append(newKPs, kp)
			}
		}

		if len(newKPs) > 0 {
			logger.Infof("batch %d/%d: creating %d accounts with trustlines", b+1, totalBatches, len(newKPs))
			ops := make([]txnbuild.Operation, 0, len(newKPs)*4)
			for _, kp := range newKPs {
				ops = append(ops, &txnbuild.CreateAccount{
					Destination: kp.Address(),
					Amount:      fundingAmount,
				})
				for _, asset := range st.Assets {
					ops = append(ops, &txnbuild.ChangeTrust{
						Line:          txnbuild.ChangeTrustAssetWrapper{Asset: asset},
						SourceAccount: kp.Address(),
					})
				}
			}
			src, err := st.RPCClient.LoadAccount(ctx, st.FeePayerKP.Address())
			if err != nil {
				return fmt.Errorf("batch %d: load fee payer: %w", b+1, err)
			}
			tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
				SourceAccount:        src,
				IncrementSequenceNum: true,
				Operations:           ops,
				BaseFee:              txnbuild.MinBaseFee,
				Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(state.TxTimeBoundSecs)},
			})
			if err != nil {
				return fmt.Errorf("batch %d: build create+trust tx: %w", b+1, err)
			}
			signers := append([]*keypair.Full{st.FeePayerKP}, newKPs...)
			tx, err = tx.Sign(cfg.NetworkPassphrase, signers...)
			if err != nil {
				return fmt.Errorf("batch %d: sign create+trust tx: %w", b+1, err)
			}
			b64, err := tx.Base64()
			if err != nil {
				return fmt.Errorf("batch %d: marshal create+trust tx: %w", b+1, err)
			}
			if state.SubmitAllAndPoll(ctx, logger, st.RPCClient, []string{b64}) > 0 {
				return fmt.Errorf("batch %d: create+trust tx failed", b+1)
			}
		}

		if len(existingKPs) > 0 {
			logger.Infof("batch %d/%d: adding trustlines to %d existing accounts", b+1, totalBatches, len(existingKPs))
			ops := make([]txnbuild.Operation, 0, len(existingKPs)*len(st.Assets))
			for j, kp := range existingKPs {
				srcAccount := ""
				if j > 0 {
					srcAccount = kp.Address()
				}
				for _, asset := range st.Assets {
					ops = append(ops, &txnbuild.ChangeTrust{
						Line:          txnbuild.ChangeTrustAssetWrapper{Asset: asset},
						SourceAccount: srcAccount,
					})
				}
			}
			src, err := st.RPCClient.LoadAccount(ctx, existingKPs[0].Address())
			if err != nil {
				return fmt.Errorf("batch %d: load existing account: %w", b+1, err)
			}
			tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
				SourceAccount:        src,
				IncrementSequenceNum: true,
				Operations:           ops,
				BaseFee:              txnbuild.MinBaseFee,
				Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(state.TxTimeBoundSecs)},
			})
			if err != nil {
				return fmt.Errorf("batch %d: build trust-only tx: %w", b+1, err)
			}
			tx, err = tx.Sign(cfg.NetworkPassphrase, existingKPs...)
			if err != nil {
				return fmt.Errorf("batch %d: sign trust-only tx: %w", b+1, err)
			}
			b64, err := tx.Base64()
			if err != nil {
				return fmt.Errorf("batch %d: marshal trust-only tx: %w", b+1, err)
			}
			if state.SubmitAllAndPoll(ctx, logger, st.RPCClient, []string{b64}) > 0 {
				return fmt.Errorf("batch %d: trust-only tx failed", b+1)
			}
		}
	}

	return nil
}

// createPassiveAccounts provisions non-SAC-holder participant accounts. These
// accounts receive XLM only and skip trustline creation.
func createPassiveAccounts(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	accountKPs []*keypair.Full,
	fundingAmount string,
) error {
	n := len(accountKPs)
	totalBatches := (n + createOnlyBatchSize - 1) / createOnlyBatchSize
	for b := range totalBatches {
		start := b * createOnlyBatchSize
		end := min(start+createOnlyBatchSize, n)
		batch := accountKPs[start:end]

		newKPs := make([]*keypair.Full, 0, len(batch))
		for _, kp := range batch {
			exists, err := state.AccountExists(ctx, st.RPCClient, kp.Address())
			if err != nil {
				return fmt.Errorf("batch %d: check %s: %w", b+1, kp.Address(), err)
			}
			if !exists {
				newKPs = append(newKPs, kp)
			}
		}

		if len(newKPs) == 0 {
			logger.Infof("batch %d/%d: passive accounts already exist, skipping", b+1, totalBatches)
			continue
		}

		ops := make([]txnbuild.Operation, 0, len(newKPs))
		for _, kp := range newKPs {
			ops = append(ops, &txnbuild.CreateAccount{
				Destination: kp.Address(),
				Amount:      fundingAmount,
			})
		}

		logger.Infof("batch %d/%d: creating %d passive accounts", b+1, totalBatches, len(newKPs))
		if err := state.SubmitAndWait(ctx, logger, st.RPCClient, cfg.NetworkPassphrase, st.FeePayerKP, state.InclusionFee, ops); err != nil {
			return fmt.Errorf("batch %d: create passive accounts: %w", b+1, err)
		}
	}
	return nil
}

// mintSACBalances sends each benchmark asset to every SAC-active account from
// the fee payer (who is also the issuer). Payments are batched to keep
// transactions small.
func mintSACBalances(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	accountKPs []*keypair.Full,
) error {
	n := len(accountKPs)
	totalBatches := (n + mintBatchSize - 1) / mintBatchSize
	for b := range totalBatches {
		start := b * mintBatchSize
		end := min(start+mintBatchSize, n)
		batch := accountKPs[start:end]

		ops := make([]txnbuild.Operation, 0, len(batch)*len(st.Assets))
		for _, kp := range batch {
			for _, asset := range st.Assets {
				ops = append(ops, &txnbuild.Payment{
					Destination: kp.Address(),
					Asset:       asset,
					Amount:      initialAccountTokenBalance,
				})
			}
		}

		logger.Infof("mint batch %d/%d (%d accounts)", b+1, totalBatches, len(batch))
		if err := state.SubmitAndWait(ctx, logger, st.RPCClient, cfg.NetworkPassphrase, st.FeePayerKP, state.InclusionFee, ops); err != nil {
			return fmt.Errorf("accounts: mint batch %d: %w", b+1, err)
		}
	}
	return nil
}
