package setup

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

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

	balances, err := ledger.FetchTrustlineBalances(ctx, st.RPCClient, st.Assets[:], holderKPs, ledger.DefaultBatchSize)
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
