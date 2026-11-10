package setup

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type accountProvisionPlan struct {
	existingCount int
	targetCount   int

	existingHolders int
	targetHolders   int

	fundingAmount string

	newKPs              []*keypair.Full
	promotedExistingKPs []*keypair.Full
	holderNewKPs        []*keypair.Full
	passiveNewKPs       []*keypair.Full
}

func (p accountProvisionPlan) satisfied() bool {
	return p.existingCount >= p.targetCount && p.existingHolders >= p.targetHolders
}

func buildAccountProvisionPlan(cfg config.Config, st *state.State) (*accountProvisionPlan, error) {
	plan := &accountProvisionPlan{
		existingCount:   len(st.AccountKPs),
		targetCount:     cfg.NumberOfAccounts,
		existingHolders: len(st.SACHolderKPs),
		fundingAmount:   fmt.Sprintf("%.7f", cfg.BaseReserveXLM),
	}
	computedTargetHolders := state.RecommendedBenchmarkSupersetHolderAccountCount(cfg, plan.targetCount)
	plan.targetHolders = max(plan.existingHolders, computedTargetHolders)
	if plan.satisfied() {
		return plan, nil
	}

	maxIdx := state.AccountSeedStartIndex - 1
	for i, kp := range st.AccountKPs {
		idx, err := state.RecoverIndex(kp)
		if err != nil {
			return nil, fmt.Errorf("existing account %d: recover derivation index: %w", i, err)
		}
		maxIdx = max(maxIdx, idx)
	}

	newCount := plan.targetCount - plan.existingCount
	plan.newKPs = make([]*keypair.Full, newCount)
	for i := range plan.newKPs {
		idx := maxIdx + 1 + i
		kp, err := state.DeriveKeypair(st.FeePayerKP, idx)
		if err != nil {
			return nil, fmt.Errorf("account %d: derive keypair: %w", plan.existingCount+i, err)
		}
		plan.newKPs[i] = kp
	}

	promoteExisting := max(0, min(plan.existingCount, plan.targetHolders)-plan.existingHolders)
	if promoteExisting > 0 {
		plan.promotedExistingKPs = st.AccountKPs[plan.existingHolders : plan.existingHolders+promoteExisting]
	}
	remainingHolderSlots := max(0, plan.targetHolders-plan.existingHolders-promoteExisting)
	remainingHolderSlots = min(remainingHolderSlots, len(plan.newKPs))
	plan.holderNewKPs = plan.newKPs[:remainingHolderSlots]
	plan.passiveNewKPs = plan.newKPs[remainingHolderSlots:]
	return plan, nil
}

func assignHolderSubset(st *state.State, holderCount int) {
	if holderCount > 0 {
		st.SACHolderKPs = make([]*keypair.Full, holderCount)
		copy(st.SACHolderKPs, st.AccountKPs[:holderCount])
		return
	}
	st.SACHolderKPs = nil
}
