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

const (
	// createAndTrustBatchSize is the number of participant accounts per
	// combined CreateAccount+ChangeTrust batch. Each account contributes 4
	// ops (1 CreateAccount + 3 ChangeTrust) and must co-sign the inner tx
	// alongside the fee payer. The Stellar XDR caps the signatures array at
	// exactly 20 (DecoratedSignature signatures<20>), so we use at most
	// 19 new accounts: 19 + 1 fee payer = 20 total signatures.
	// Op count: 19 * 4 = 76, well under the 100-op limit.
	createAndTrustBatchSize = 19

	// mintBatchSize is the number of accounts funded per Payment transaction.
	// 3 assets * 33 accounts = 99 ops, just under the 100-op limit.
	mintBatchSize = 33

	// initialAccountTokenBalance is the starting balance of each benchmark
	// asset minted to every participant account.
	initialAccountTokenBalance = "1000000.0000000"
)

type accountsStep struct{}

func (accountsStep) Name() string { return "create participant accounts" }

// Run creates cfg.NumberOfAccounts participant accounts. Each account is:
//   - Funded with cfg.BaseReserveXLM (covers 2 base reserves + 3 trustlines).
//   - Given a trustline for each of the 3 benchmark assets.
//   - Minted an initial balance of each asset from the fee-payer/issuer.
//
// Accounts are derived deterministically from the fee-payer seed so the setup
// is idempotent across restarts. All transactions are submitted serially from
// the fee payer.
func (accountsStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	existingCount := len(st.AccountKPs)
	targetCount := cfg.NumberOfAccounts

	if existingCount >= targetCount {
		logger.Infof("already have %d accounts (target %d)  -- nothing to do", existingCount, targetCount)
		return nil
	}

	// Derive only the new keypairs (deterministic from fee payer seed).
	newCount := targetCount - existingCount
	newKPs := make([]*keypair.Full, newCount)
	for i := range newKPs {
		idx := state.AccountSeedStartIndex + existingCount + i
		kp, err := state.DeriveKeypair(st.FeePayerKP, idx)
		if err != nil {
			return fmt.Errorf("account %d: derive keypair: %w", existingCount+i, err)
		}
		newKPs[i] = kp
	}

	if existingCount > 0 {
		logger.Infof("existing=%d, target=%d  -- creating %d additional accounts", existingCount, targetCount, newCount)
	} else {
		logger.Infof("derived %d keypairs", targetCount)
	}

	// --- CreateAccount + ChangeTrust for new accounts --------------------
	fundingAmount := fmt.Sprintf("%.7f", cfg.BaseReserveXLM)
	if err := createAccounts(ctx, logger, cfg, st, newKPs, fundingAmount); err != nil {
		return err
	}

	// --- Mint initial token balances for new accounts --------------------
	if err := mintBalances(ctx, logger, cfg, st, newKPs); err != nil {
		return err
	}

	st.AccountKPs = append(st.AccountKPs, newKPs...)
	return nil
}

// createAccounts provisions participant accounts serially via the fee payer.
// Each batch combines CreateAccount + ChangeTrust for new accounts (or
// trust-only for accounts that already exist from a previous partial run).
func createAccounts(
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

		// Separate new accounts from those that already exist.
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

		// Combined CreateAccount + ChangeTrust for new accounts.
		// Fee payer is the tx source (and funder); each new account co-signs
		// its own ChangeTrust ops.
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

		// Trust-only for accounts that already exist (idempotent re-run).
		// First existing account is the tx source; the rest set SourceAccount.
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

// mintBalances sends each benchmark asset to every account from the fee payer
// (who is also the issuer). Payments are batched to keep transactions small.
func mintBalances(
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
