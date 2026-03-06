package tx

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/txnbuild"
)

// BuildSendTxB64 builds a signed base64-encoded transaction for a self-payment used for sendTransaction calls.
func BuildSendTxB64(acct *WorkerAccount, seq int64, networkPassphrase string) (string, error) {
	op := txnbuild.Payment{
		Destination: acct.Keypair.Address(), // self-payment
		Amount:      "1",
		Asset:       txnbuild.NativeAsset{},
	}

	sa := txnbuild.NewSimpleAccount(acct.Keypair.Address(), seq)
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

	// sign with acct.Keypair, return txn.Base64()
	txn, err = txn.Sign(networkPassphrase, acct.Keypair)
	if err != nil {
		return "", fmt.Errorf("couldn't sign transaction: %w", err)
	}
	return txn.Base64()
}
