package setup

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

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

	// createOnlyBatchSize is the number of non-SAC-holder accounts created per
	// batch. These accounts do not need trustlines, so a larger pure
	// CreateAccount batch is safe under the 100-op limit.
	createOnlyBatchSize = 100

	// mintBatchSize is the number of accounts funded per Payment transaction.
	// 3 assets * 33 accounts = 99 ops, just under the 100-op limit.
	mintBatchSize = 33

	// initialAccountTokenBalance is the starting balance of each benchmark
	// asset minted to every participant account.
	initialAccountTokenBalance = "1000000.0000000"
)

type accountsStep struct{}

func (accountsStep) Name() string { return "create participant accounts" }

// Run creates cfg.NumberOfAccounts participant accounts. A formula-derived
// prefix of the participant set forms the trustlined / asset-funded holder
// superset needed to support every benchmark mode with the requested rates
// and duration, and is:
//   - Funded with cfg.BaseReserveXLM (covers 2 base reserves + 3 trustlines).
//   - Given a trustline for each of the 3 benchmark assets.
//   - Minted an initial balance of each asset from the fee-payer/issuer.
//
// Remaining accounts are created and funded with XLM only. They are still
// available for non-SAC workloads such as OZ transfers.
//
// Accounts are derived deterministically from the fee-payer seed so the setup
// is idempotent across restarts. All transactions are submitted serially from
// the fee payer.
func (accountsStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	existingCount := len(st.AccountKPs)
	targetCount := cfg.NumberOfAccounts
	existingHolders := len(st.SACHolderKPs)
	fundingAmount := fmt.Sprintf("%.7f", cfg.BaseReserveXLM)
	if existingHolders == 0 && existingCount > 0 {
		st.SACHolderKPs = state.DefaultSACHolderKPs(st.AccountKPs)
		existingHolders = len(st.SACHolderKPs)
	}
	computedTargetHolders := state.RecommendedBenchmarkSupersetHolderAccountCount(cfg, targetCount)
	targetHolders := max(existingHolders, computedTargetHolders)

	if existingCount >= targetCount && existingHolders >= targetHolders {
		if targetHolders > 0 {
			st.SACHolderKPs = make([]*keypair.Full, targetHolders)
			copy(st.SACHolderKPs, st.AccountKPs[:targetHolders])
		} else {
			st.SACHolderKPs = nil
		}
		if err := reconcileSACHolderAccounts(ctx, logger, cfg, st, st.SACHolderKPs, fundingAmount); err != nil {
			return err
		}
		if err := reconcilePassiveAccounts(ctx, logger, cfg, st, passiveParticipantAccounts(st.AccountKPs, targetHolders), fundingAmount); err != nil {
			return err
		}
		logger.Infof("already have %d accounts (target %d) and %d holder accounts (target %d)  -- reconciled ledger state", existingCount, targetCount, existingHolders, targetHolders)
		st.PendingOZMintKPs = nil
		return nil
	}

	maxIdx := state.AccountSeedStartIndex - 1
	for i, kp := range st.AccountKPs {
		idx, err := state.RecoverIndex(kp)
		if err != nil {
			return fmt.Errorf("existing account %d: recover derivation index: %w", i, err)
		}
		maxIdx = max(maxIdx, idx)
	}

	// Derive only the new keypairs (deterministic from fee payer seed).
	newCount := targetCount - existingCount
	newKPs := make([]*keypair.Full, newCount)
	for i := range newKPs {
		idx := maxIdx + 1 + i
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

	promoteExisting := max(0, min(existingCount, targetHolders)-existingHolders)
	var promotedExistingKPs []*keypair.Full
	if promoteExisting > 0 {
		promotedExistingKPs = st.AccountKPs[existingHolders : existingHolders+promoteExisting]
	}
	remainingHolderSlots := max(0, targetHolders-existingHolders-promoteExisting)
	remainingHolderSlots = min(remainingHolderSlots, len(newKPs))
	holderNewKPs := newKPs[:remainingHolderSlots]
	passiveNewKPs := newKPs[remainingHolderSlots:]

	logger.Infof(
		"holder subset: %d existing -> %d target; promote=%d new holders=%d passive=%d",
		existingHolders, targetHolders, len(promotedExistingKPs), len(holderNewKPs), len(passiveNewKPs),
	)

	// --- Promote existing passive accounts into holder accounts ---------
	if len(promotedExistingKPs) > 0 {
		if err := createSACHolderAccounts(ctx, logger, cfg, st, promotedExistingKPs, fundingAmount); err != nil {
			return err
		}
		if err := mintSACBalances(ctx, logger, cfg, st, promotedExistingKPs); err != nil {
			return err
		}
	}

	// --- CreateAccount + ChangeTrust for new SAC-active accounts ---------
	if len(holderNewKPs) > 0 {
		if err := createSACHolderAccounts(ctx, logger, cfg, st, holderNewKPs, fundingAmount); err != nil {
			return err
		}
	}

	// --- Create passive accounts with XLM only ---------------------------
	if len(passiveNewKPs) > 0 {
		if err := createPassiveAccounts(ctx, logger, cfg, st, passiveNewKPs, fundingAmount); err != nil {
			return err
		}
	}

	// --- Mint initial token balances only to new SAC-active accounts -----
	if len(holderNewKPs) > 0 {
		if err := mintSACBalances(ctx, logger, cfg, st, holderNewKPs); err != nil {
			return err
		}
	}

	st.AccountKPs = append(st.AccountKPs, newKPs...)
	if targetHolders > 0 {
		st.SACHolderKPs = make([]*keypair.Full, targetHolders)
		copy(st.SACHolderKPs, st.AccountKPs[:targetHolders])
	} else {
		st.SACHolderKPs = nil
	}
	if err := reconcileSACHolderAccounts(ctx, logger, cfg, st, st.SACHolderKPs, fundingAmount); err != nil {
		return err
	}
	if err := reconcilePassiveAccounts(ctx, logger, cfg, st, passiveParticipantAccounts(st.AccountKPs, targetHolders), fundingAmount); err != nil {
		return err
	}
	st.PendingOZMintKPs = newKPs
	return nil
}

func passiveParticipantAccounts(accountKPs []*keypair.Full, holderCount int) []*keypair.Full {
	if holderCount < 0 {
		holderCount = 0
	}
	if holderCount >= len(accountKPs) {
		return nil
	}
	return accountKPs[holderCount:]
}

func reconcileSACHolderAccounts(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	holderKPs []*keypair.Full,
	fundingAmount string,
) error {
	if len(holderKPs) == 0 {
		return nil
	}

	balances, err := fetchSetupTrustlineBalances(ctx, st, holderKPs)
	if err != nil {
		return fmt.Errorf("accounts: fetch holder trustlines for reconciliation: %w", err)
	}

	trustlineRepairKPs, mintPlan := planSACHolderRepairs(st, holderKPs, balances)
	if len(trustlineRepairKPs) == 0 && len(mintPlan) == 0 {
		return nil
	}
	if len(trustlineRepairKPs) > 0 {
		logger.Warnf("repairing %d holder accounts with missing trustlines", len(trustlineRepairKPs))
		if err := createSACHolderAccounts(ctx, logger, cfg, st, trustlineRepairKPs, fundingAmount); err != nil {
			return err
		}
	}
	if len(mintPlan) > 0 {
		logger.Warnf("repairing holder balances on %d accounts with missing or zero asset balances", len(mintPlan))
		if err := mintMissingSACBalances(ctx, logger, cfg, st, holderKPs, mintPlan); err != nil {
			return err
		}
	}
	return nil
}

func reconcilePassiveAccounts(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	passiveKPs []*keypair.Full,
	fundingAmount string,
) error {
	if len(passiveKPs) == 0 {
		return nil
	}

	missingKPs := make([]*keypair.Full, 0)
	for _, kp := range passiveKPs {
		exists, err := state.AccountExists(ctx, st.RPCClient, kp.Address())
		if err != nil {
			return fmt.Errorf("accounts: check passive account %s: %w", kp.Address(), err)
		}
		if !exists {
			missingKPs = append(missingKPs, kp)
		}
	}
	if len(missingKPs) == 0 {
		return nil
	}

	logger.Warnf("repairing %d passive accounts missing on-ledger", len(missingKPs))
	return createPassiveAccounts(ctx, logger, cfg, st, missingKPs, fundingAmount)
}

func planSACHolderRepairs(
	st *state.State,
	holderKPs []*keypair.Full,
	balances map[string]map[string]xdr.Int64,
) ([]*keypair.Full, map[string][]txnbuild.CreditAsset) {
	trustlineRepairKPs := make([]*keypair.Full, 0)
	mintPlan := make(map[string][]txnbuild.CreditAsset)

	for _, kp := range holderKPs {
		accountBalances := balances[kp.Address()]
		missingTrustline := false
		for _, asset := range st.Assets {
			balance, ok := accountBalances[asset.GetCode()]
			if !ok {
				missingTrustline = true
			}
			if !ok || balance == 0 {
				mintPlan[kp.Address()] = append(mintPlan[kp.Address()], asset)
			}
		}
		if missingTrustline {
			trustlineRepairKPs = append(trustlineRepairKPs, kp)
		}
	}

	return trustlineRepairKPs, mintPlan
}

func mintMissingSACBalances(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	st *state.State,
	holderKPs []*keypair.Full,
	mintPlan map[string][]txnbuild.CreditAsset,
) error {
	if len(mintPlan) == 0 {
		return nil
	}

	type mintOp struct {
		address string
		asset   txnbuild.CreditAsset
	}
	opsToSend := make([]mintOp, 0)
	for _, kp := range holderKPs {
		for _, asset := range mintPlan[kp.Address()] {
			opsToSend = append(opsToSend, mintOp{address: kp.Address(), asset: asset})
		}
	}

	if len(opsToSend) == 0 {
		return nil
	}

	totalBatches := (len(opsToSend) + 99) / 100
	for b := range totalBatches {
		start := b * 100
		end := min(start+100, len(opsToSend))
		batch := opsToSend[start:end]

		ops := make([]txnbuild.Operation, 0, len(batch))
		for _, item := range batch {
			ops = append(ops, &txnbuild.Payment{
				Destination: item.address,
				Asset:       item.asset,
				Amount:      initialAccountTokenBalance,
			})
		}

		logger.Infof("repair mint batch %d/%d (%d payments)", b+1, totalBatches, len(batch))
		if err := state.SubmitAndWait(ctx, logger, st.RPCClient, cfg.NetworkPassphrase, st.FeePayerKP, state.InclusionFee, ops); err != nil {
			return fmt.Errorf("accounts: repair mint batch %d: %w", b+1, err)
		}
	}
	return nil
}

func fetchSetupTrustlineBalances(
	ctx context.Context,
	st *state.State,
	accounts []*keypair.Full,
) (map[string]map[string]xdr.Int64, error) {
	type keyMeta struct{ account, assetCode string }

	keys := make([]string, 0, len(accounts)*len(st.Assets))
	metas := make([]keyMeta, 0, len(accounts)*len(st.Assets))
	for _, kp := range accounts {
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

	entries, err := fetchSetupLedgerEntriesByKey(ctx, st.RPCClient, keys)
	if err != nil {
		return nil, fmt.Errorf("get trustline entries: %w", err)
	}

	keyToMeta := make(map[string]keyMeta, len(keys))
	for i, key := range keys {
		keyToMeta[key] = metas[i]
	}

	result := make(map[string]map[string]xdr.Int64)
	for key, entry := range entries {
		meta, ok := keyToMeta[key]
		if !ok {
			continue
		}
		var data xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(entry.DataXDR, &data); err != nil {
			continue
		}
		if data.TrustLine == nil {
			continue
		}
		if result[meta.account] == nil {
			result[meta.account] = make(map[string]xdr.Int64)
		}
		result[meta.account][meta.assetCode] = data.TrustLine.Balance
	}
	return result, nil
}

func fetchSetupLedgerEntriesByKey(
	ctx context.Context,
	rpc interface {
		GetLedgerEntries(context.Context, protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
	},
	keys []string,
) (map[string]protocol.LedgerEntryResult, error) {
	const batchSize = 100
	entries := make(map[string]protocol.LedgerEntryResult, len(keys))
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys[start:end]})
		if err != nil {
			return nil, err
		}
		for _, entry := range resp.Entries {
			entries[entry.KeyXDR] = entry
		}
	}
	return entries, nil
}

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
