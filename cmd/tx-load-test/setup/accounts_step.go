package setup

import (
	"context"

	"github.com/stellar/go-stellar-sdk/support/log"

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
	if len(st.SACHolderKPs) == 0 && len(st.AccountKPs) > 0 {
		st.SACHolderKPs = state.DefaultSACHolderKPs(st.AccountKPs)
	}

	plan, err := buildAccountProvisionPlan(cfg, st)
	if err != nil {
		return err
	}

	if plan.satisfied() {
		assignHolderSubset(st, plan.targetHolders)
		if err := reconcileSACHolderAccounts(ctx, logger, cfg, st, st.SACHolderKPs, plan.fundingAmount); err != nil {
			return err
		}
		if err := reconcilePassiveAccounts(ctx, logger, cfg, st, passiveParticipantAccounts(st.AccountKPs, plan.targetHolders), plan.fundingAmount); err != nil {
			return err
		}
		logger.Infof("already have %d accounts (target %d) and %d holder accounts (target %d)  -- reconciled ledger state", plan.existingCount, plan.targetCount, plan.existingHolders, plan.targetHolders)
		st.PendingOZMintKPs = nil
		return nil
	}

	if plan.existingCount > 0 {
		logger.Infof("existing=%d, target=%d  -- creating %d additional accounts", plan.existingCount, plan.targetCount, len(plan.newKPs))
	} else {
		logger.Infof("derived %d keypairs", plan.targetCount)
	}
	logger.Infof(
		"holder subset: %d existing -> %d target; promote=%d new holders=%d passive=%d",
		plan.existingHolders, plan.targetHolders, len(plan.promotedExistingKPs), len(plan.holderNewKPs), len(plan.passiveNewKPs),
	)

	if len(plan.promotedExistingKPs) > 0 {
		if err := createSACHolderAccounts(ctx, logger, cfg, st, plan.promotedExistingKPs, plan.fundingAmount); err != nil {
			return err
		}
		if err := mintSACBalances(ctx, logger, cfg, st, plan.promotedExistingKPs); err != nil {
			return err
		}
	}
	if len(plan.holderNewKPs) > 0 {
		if err := createSACHolderAccounts(ctx, logger, cfg, st, plan.holderNewKPs, plan.fundingAmount); err != nil {
			return err
		}
	}
	if len(plan.passiveNewKPs) > 0 {
		if err := createPassiveAccounts(ctx, logger, cfg, st, plan.passiveNewKPs, plan.fundingAmount); err != nil {
			return err
		}
	}
	if len(plan.holderNewKPs) > 0 {
		if err := mintSACBalances(ctx, logger, cfg, st, plan.holderNewKPs); err != nil {
			return err
		}
	}

	st.AccountKPs = append(st.AccountKPs, plan.newKPs...)
	assignHolderSubset(st, plan.targetHolders)
	if err := reconcileSACHolderAccounts(ctx, logger, cfg, st, st.SACHolderKPs, plan.fundingAmount); err != nil {
		return err
	}
	if err := reconcilePassiveAccounts(ctx, logger, cfg, st, passiveParticipantAccounts(st.AccountKPs, plan.targetHolders), plan.fundingAmount); err != nil {
		return err
	}
	st.PendingOZMintKPs = plan.newKPs
	return nil
}
