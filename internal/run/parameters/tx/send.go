package tx

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/txnbuild"
)

func BuildSendPaymentTx(from *WorkerAccount, to *WorkerAccount, amount int64, seq int64, networkPassphrase string) (*txnbuild.Transaction, error) {
	op := txnbuild.Payment{
		Destination: to.Keypair.Address(),
		Amount:      fmt.Sprintf("%d", amount),
		Asset:       txnbuild.NativeAsset{},
	}
	return buildTx([]txnbuild.Operation{&op}, from, seq, networkPassphrase)
}

// BuildAccountMergeTx builds a tx that closes the source account and transfers all remaining funds to the destination.
func BuildAccountMergeTx(from *WorkerAccount, to *WorkerAccount, seq int64, networkPassphrase string) (*txnbuild.Transaction, error) {
	op := txnbuild.AccountMerge{
		Destination: to.Keypair.Address(),
	}
	return buildTx([]txnbuild.Operation{&op}, from, seq, networkPassphrase)
}

// BuildCreateAccountsTx builds a transaction that creates and funds new accounts.
// Used to fund worker accounts that don't yet exist on-chain.
func BuildCreateAccountsTx(
	from *WorkerAccount,
	to []*WorkerAccount,
	amount int64,
	seq int64,
	networkPassphrase string,
) (*txnbuild.Transaction, error) {
	ops := make([]txnbuild.Operation, 0, len(to))
	for _, acct := range to {
		op := txnbuild.CreateAccount{
			Destination: acct.Keypair.Address(),
			Amount:      fmt.Sprintf("%d", amount),
		}
		ops = append(ops, &op)
	}
	return buildTx(ops, from, seq, networkPassphrase)
}

func BuildSelfSendTx(acct *WorkerAccount, seq int64, networkPassphrase string) (*txnbuild.Transaction, error) {
	return BuildSendPaymentTx(acct, acct, 1, seq, networkPassphrase)
}

// BuildSelfSendTxB64 builds a base64-encoded transaction for a 1 XLM self-payment used for sendTransaction calls.
func BuildSelfSendTxB64(acct *WorkerAccount, seq int64, networkPassphrase string) (string, error) {
	txn, err := BuildSelfSendTx(acct, seq, networkPassphrase)
	if err != nil {
		return "", err
	}
	return txn.Base64()
}

func buildTx(
	ops []txnbuild.Operation,
	from *WorkerAccount,
	seq int64,
	networkPassphrase string,
) (*txnbuild.Transaction, error) {
	sa := txnbuild.NewSimpleAccount(from.Keypair.Address(), seq)
	txn, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sa,
		IncrementSequenceNum: true,
		Operations:           ops,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't create transaction: %w", err)
	}

	txn, err = txn.Sign(networkPassphrase, from.Keypair)
	if err != nil {
		return nil, fmt.Errorf("couldn't sign transaction: %w", err)
	}
	return txn, nil
}
