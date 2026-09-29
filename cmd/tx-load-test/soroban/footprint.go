package soroban

import "github.com/stellar/go-stellar-sdk/xdr"

func ReplaceFootprintReadWriteKeys(tmpl xdr.LedgerFootprint, readWrite ...xdr.LedgerKey) xdr.LedgerFootprint {
	return xdr.LedgerFootprint{
		ReadOnly:  append([]xdr.LedgerKey(nil), tmpl.ReadOnly...),
		ReadWrite: append([]xdr.LedgerKey(nil), readWrite...),
	}
}
