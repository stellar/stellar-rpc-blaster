package ledger

import (
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ContractScAddress wraps a contract ID in an ScAddress.
func ContractScAddress(contractID xdr.ContractId) xdr.ScAddress {
	return xdr.ScAddress{
		Type:       xdr.ScAddressTypeScAddressTypeContract,
		ContractId: &contractID,
	}
}

// ContractInstanceLedgerKey builds the persistent contract-instance ledger key
// for a contract.
func ContractInstanceLedgerKey(contractID xdr.ContractId) xdr.LedgerKey {
	return xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract:   ContractScAddress(contractID),
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}
}

// ContractCodeLedgerKey builds the ledger key for a contract's uploaded Wasm
// code entry.
func ContractCodeLedgerKey(hash xdr.Hash) xdr.LedgerKey {
	return xdr.LedgerKey{
		Type:         xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.LedgerKeyContractCode{Hash: hash},
	}
}

// ContractBalanceLedgerKey builds the persistent Balance(holder) contract-data
// key used by both the SAC (for contract-held balances) and the OZ fungible
// token: ContractData(contract, Vec[Symbol("Balance"), Address(holder)]).
func ContractBalanceLedgerKey(contractID xdr.ContractId, holder xdr.ScAddress) xdr.LedgerKey {
	balanceVariant := xdr.ScSymbol("Balance")
	balanceKeyVec := xdr.ScVec{
		{Type: xdr.ScValTypeScvSymbol, Sym: &balanceVariant},
		{Type: xdr.ScValTypeScvAddress, Address: &holder},
	}
	balanceKeyRef := &balanceKeyVec

	return xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract:   ContractScAddress(contractID),
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &balanceKeyRef},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}
}

// WasmHashFromInstance extracts the Wasm hash from a contract-instance ledger
// entry. Returns nil for non-instance entries and for native executables
// (SACs), which have no separate code entry.
func WasmHashFromInstance(data *xdr.LedgerEntryData) *xdr.Hash {
	if data == nil || data.Type != xdr.LedgerEntryTypeContractData || data.ContractData == nil {
		return nil
	}
	val := data.ContractData.Val
	if val.Type != xdr.ScValTypeScvContractInstance || val.Instance == nil {
		return nil
	}
	if val.Instance.Executable.Type != xdr.ContractExecutableTypeContractExecutableWasm {
		return nil
	}
	return val.Instance.Executable.WasmHash
}
