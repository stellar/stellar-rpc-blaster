package tx

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// BuildSimulateTxB64 takes a contract ID and builds a base64-encoded transaction envelope
// used for simulateTransaction calls targeting that contract.
func BuildSimulateTxB64(contractStrkey string) (string, error) {
	contractID, err := decodeContractID(contractStrkey)
	if err != nil {
		return "", err
	}

	contractAddr := xdr.ScAddress{
		Type:       xdr.ScAddressTypeScAddressTypeContract,
		ContractId: &contractID,
	}

	op := txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: contractAddr,
				FunctionName:    "transfer", // arbitrary; WASM errors at runtime, not before
				Args:            xdr.ScVec{},
			},
		},
		Ext: xdr.TransactionExt{
			V: 1,
			SorobanData: &xdr.SorobanTransactionData{
				Resources: xdr.SorobanResources{
					Footprint: xdr.LedgerFootprint{
						ReadOnly: []xdr.LedgerKey{{
							Type: xdr.LedgerEntryTypeContractData,
							ContractData: &xdr.LedgerKeyContractData{
								Contract:   contractAddr,
								Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
								Durability: xdr.ContractDataDurabilityPersistent,
							},
						}},
					},
					Instructions:  500_000,
					DiskReadBytes: 2_000,
					WriteBytes:    0,
				},
				ResourceFee: 10_000_000,
			},
		},
	}

	source := zeroAccountID()
	sa := txnbuild.NewSimpleAccount(source, 0)
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sa,
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimebounds(0, 0)},
	})
	if err != nil {
		return "", fmt.Errorf("couldn't build simulate tx for %s: %w", contractStrkey, err)
	}
	return tx.Base64()
}

func decodeContractID(contractStrkey string) (xdr.ContractId, error) {
	decoded, err := strkey.Decode(strkey.VersionByteContract, contractStrkey)
	if err != nil {
		return xdr.ContractId{}, fmt.Errorf("invalid contract strkey %q: %w", contractStrkey, err)
	}
	var id xdr.ContractId
	copy(id[:], decoded)
	return id, nil
}

func zeroAccountID() string {
	return strkey.MustEncode(strkey.VersionByteAccountID, make([]byte, 32))
}
