package tx

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// BuildSimulateTxB64 builds a base64-encoded PaymentToContract used for simulateTransaction calls.
// Uses zero contract/source accounts - no real accounts needed since this is never submitted on-chain.
func BuildSimulateTxB64(networkPassphrase string) (string, error) {
	var (
		contractId      xdr.ContractId
		sourceAccountId string
		destId          string
		err             error
	)
	if sourceAccountId, err = strkey.Encode(strkey.VersionByteAccountID, make([]byte, 32)); err != nil {
		return "", fmt.Errorf("couldn't encode source account ID: %w", err)
	}
	if destId, err = strkey.Encode(strkey.VersionByteContract, contractId[:]); err != nil {
		return "", fmt.Errorf("couldn't encode destination ID: %w", err)
	}

	op, err := txnbuild.NewPaymentToContract(txnbuild.PaymentToContractParams{
		NetworkPassphrase: networkPassphrase,
		Destination:       destId,
		Amount:            "10",
		Asset:             txnbuild.NativeAsset{},
		SourceAccount:     sourceAccountId,
		Fees:              nil, // use default fees
	})
	if err != nil {
		return "", fmt.Errorf("couldn't build payment to contract op: %w", err)
	}

	sa := txnbuild.NewSimpleAccount(sourceAccountId, 0)
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sa,
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimebounds(0, 0)},
	})
	if err != nil {
		return "", fmt.Errorf("couldn't build simulate tx: %w", err)
	}
	return tx.Base64()
}
