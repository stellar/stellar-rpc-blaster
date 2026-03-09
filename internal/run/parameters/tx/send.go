package tx

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/txnbuild"
)

func BuildSendPaymentTxB64(from *WorkerAccount, to *WorkerAccount, amount int64, seq int64, networkPassphrase string) (string, error) {
	op := txnbuild.Payment{
		Destination: to.Keypair.Address(),
		Amount:      fmt.Sprintf("%d", amount),
		Asset:       txnbuild.NativeAsset{},
	}
	return buildTxB64(&op, from, seq, networkPassphrase)
}

// BuildAccountMergeTxB64 builds a tx that closes the source account and transfers all remaining funds to the destination.
func BuildAccountMergeTxB64(from *WorkerAccount, to *WorkerAccount, seq int64, networkPassphrase string) (string, error) {
	op := txnbuild.AccountMerge{
		Destination: to.Keypair.Address(),
	}
	return buildTxB64(&op, from, seq, networkPassphrase)
}

// BuildCreateAccountTxB64 builds a base64-encoded transaction that creates and funds a new account.
// Used to fund worker accounts that don't yet exist on-chain.
func BuildCreateAccountTxB64(
	from *WorkerAccount,
	to *WorkerAccount,
	amount int64,
	seq int64,
	networkPassphrase string,
) (string, error) {
	op := txnbuild.CreateAccount{
		Destination: to.Keypair.Address(),
		Amount:      fmt.Sprintf("%d", amount),
	}
	return buildTxB64(&op, from, seq, networkPassphrase)
}

// BuildSelfSendTxB64 builds a base64-encoded transaction for a 1 XLM self-payment used for sendTransaction calls.
func BuildSelfSendTxB64(acct *WorkerAccount, seq int64, networkPassphrase string) (string, error) {
	return BuildSendPaymentTxB64(acct, acct, 1, seq, networkPassphrase)
}

func buildTxB64(op txnbuild.Operation, from *WorkerAccount, seq int64, networkPassphrase string) (string, error) {
	sa := txnbuild.NewSimpleAccount(from.Keypair.Address(), seq)
	txn, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sa,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	if err != nil {
		return "", fmt.Errorf("couldn't create transaction: %w", err)
	}

	txn, err = txn.Sign(networkPassphrase, from.Keypair)
	if err != nil {
		return "", fmt.Errorf("couldn't sign transaction: %w", err)
	}
	return txn.Base64()
}
