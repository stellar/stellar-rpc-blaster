package tx

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/txnbuild"
)

func BuildSendPaymentTxB64(from *WorkerAccount, to *WorkerAccount, amount uint64, seq int64, networkPassphrase string) (string, error) {
	op := txnbuild.Payment{
		Destination: to.Keypair.Address(),
		Amount:      fmt.Sprintf("%d", amount),
		Asset:       txnbuild.NativeAsset{},
	}
	sa := txnbuild.NewSimpleAccount(from.Keypair.Address(), seq)
	txn, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sa,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	if err != nil {
		return "", fmt.Errorf("couldn't create transaction: %w", err)
	}

	// sign with from.Keypair, return txn.Base64()
	txn, err = txn.Sign(networkPassphrase, from.Keypair)
	if err != nil {
		return "", fmt.Errorf("couldn't sign transaction: %w", err)
	}
	return txn.Base64()
}

// BuildSelfSendTxB64 builds a signed base64-encoded transaction for a self-payment used for sendTransaction calls.
func BuildSelfSendTxB64(acct *WorkerAccount, seq int64, networkPassphrase string) (string, error) {
	return BuildSendPaymentTxB64(acct, acct, 1, seq, networkPassphrase)
}
