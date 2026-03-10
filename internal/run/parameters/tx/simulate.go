package tx

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// BuildSimulateTxB64 builds a base64-encoded transaction envelope used for simulateTransaction calls.
// Uses zero contract/source accounts — no real accounts needed since this is never submitted on-chain.
func BuildSimulateTxB64() (string, error) {
	var contractID xdr.ContractId // 32 zero bytes
	contractAddr := xdr.ScAddress{
		Type:       xdr.ScAddressTypeScAddressTypeContract,
		ContractId: &contractID,
	}

	op := BuildInvokeHostFunction(contractAddr, xdr.ScSymbol("transfer"), xdr.ScVec{})

	sa := txnbuild.NewSimpleAccount(strkey.MustEncode(strkey.VersionByteAccountID, make([]byte, 32)), 0)
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

// Build a simple InvokeHostFunction tx touching contract storage and erroring only at execution
func BuildInvokeHostFunction(
	contractAddr xdr.ScAddress,
	functionName xdr.ScSymbol,
	args []xdr.ScVal,
) txnbuild.InvokeHostFunction {
	return txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: contractAddr,
				FunctionName:    functionName,
				Args:            xdr.ScVec(args),
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
}
