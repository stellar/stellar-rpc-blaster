package benchmark

import (
	"context"
	"fmt"
	"math/rand/v2"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const simplePaymentAmount = "0.0000001"

func verifyHolderTrustlinesReady(ctx context.Context, st *state.State, holderAccounts []*keypair.Full, label string) error {
	return verifyTrustlineBalancesReady(ctx, st, holderAccounts, label)
}

func newSimplePaymentTargeter(ctx context.Context, rpcURL string, st *state.State, accounts accountLeaseManager, recipientAccounts []*keypair.Full) (vegeta.Targeter, error) {
	if len(accounts.Accounts(accountRequirementAnySource)) == 0 {
		return nil, fmt.Errorf("need at least 1 source account for simple payments")
	}
	if len(recipientAccounts) < 2 {
		return nil, fmt.Errorf("need at least 2 participant accounts for simple payments, got %d", len(recipientAccounts))
	}

	networkPassphrase := st.NetworkPassphrase

	return func(t *vegeta.Target) error {
		lease, err := accounts.Acquire(ctx, accountRequirementAnySource)
		if err != nil {
			return fmt.Errorf("lease simple-payment source account: %w", err)
		}
		releaseLease := true
		defer func() {
			if releaseLease {
				accounts.ReleaseRetryable(lease.RequestID)
			}
		}()

		srcKP := lease.Account
		ops := make([]txnbuild.Operation, 0, state.SimplePaymentOpsPerTransaction)
		for range state.SimplePaymentOpsPerTransaction {
			dstIdx := rand.IntN(len(recipientAccounts) - 1)
			if recipientAccounts[dstIdx].Address() == srcKP.Address() {
				dstIdx++
			}
			dstKP := recipientAccounts[dstIdx]
			ops = append(ops, &txnbuild.Payment{
				Destination: dstKP.Address(),
				Asset:       txnbuild.NativeAsset{},
				Amount:      simplePaymentAmount,
			})
		}

		tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount: &txnbuild.SimpleAccount{
				AccountID: srcKP.Address(),
				Sequence:  lease.Sequence,
			},
			IncrementSequenceNum: false,
			Operations:           ops,
			BaseFee:              benchmarkBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(benchmarkTransactionTimeoutSecs)},
		})
		if err != nil {
			return fmt.Errorf("build transaction: %w", err)
		}

		body, err := buildBenchmarkSendTransactionBody(lease.RequestID, networkPassphrase, st.FeePayerKP, tx, srcKP)
		if err != nil {
			return err
		}

		populateJSONRPCTarget(t, rpcURL, body, lease.RequestID)
		releaseLease = false
		return nil
	}, nil
}
