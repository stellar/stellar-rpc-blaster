package soroban

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

func AddressScVal(address string) (xdr.ScVal, error) {
	if accountID, err := xdr.AddressToAccountId(address); err == nil {
		return xdr.ScVal{
			Type: xdr.ScValTypeScvAddress,
			Address: &xdr.ScAddress{
				Type:      xdr.ScAddressTypeScAddressTypeAccount,
				AccountId: &accountID,
			},
		}, nil
	}

	contractID, err := ledger.DecodeContractID(address)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("decode address %s: not an account or contract address", address)
	}
	return xdr.ScVal{
		Type: xdr.ScValTypeScvAddress,
		Address: &xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		},
	}, nil
}

func ContractAddressArgs(addresses ...string) (xdr.ScVec, error) {
	args := make(xdr.ScVec, 0, len(addresses))
	for _, address := range addresses {
		val, err := AddressScVal(address)
		if err != nil {
			return nil, fmt.Errorf("encode contract address %s: %w", address, err)
		}
		args = append(args, val)
	}
	return args, nil
}

func ScValContractAddress(value xdr.ScVal) (string, error) {
	address, ok := value.GetAddress()
	if !ok {
		return "", fmt.Errorf("expected address return value, got %s", value.Type.String())
	}
	contractID, ok := address.GetContractId()
	if !ok {
		return "", fmt.Errorf("expected contract address return value, got %s", address.Type.String())
	}
	encoded, err := ledger.EncodeContractID(contractID)
	if err != nil {
		return "", fmt.Errorf("encode contract address: %w", err)
	}
	return encoded, nil
}

func ScValAccountAddress(value xdr.ScVal) (string, error) {
	address, ok := value.GetAddress()
	if !ok {
		return "", fmt.Errorf("expected address return value, got %s", value.Type.String())
	}
	accountID, ok := address.GetAccountId()
	if !ok {
		return "", fmt.Errorf("expected account address return value, got %s", address.Type.String())
	}
	encoded, err := accountID.GetAddress()
	if err != nil {
		return "", fmt.Errorf("encode account address: %w", err)
	}
	return encoded, nil
}

func StringScVal(value string) xdr.ScVal {
	str := xdr.ScString(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &str}
}

func BytesScVal(value []byte) xdr.ScVal {
	b := xdr.ScBytes(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}
}

func I128ScVal(value int64) xdr.ScVal {
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(value)}}
}

func U64ScVal(value uint64) xdr.ScVal {
	v := xdr.Uint64(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}
}

func SourceAccountContractAuth(invokeArgs xdr.InvokeContractArgs) []xdr.SorobanAuthorizationEntry {
	return []xdr.SorobanAuthorizationEntry{{
		Credentials: xdr.SorobanCredentials{
			Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
		},
		RootInvocation: xdr.SorobanAuthorizedInvocation{
			Function: xdr.SorobanAuthorizedFunction{
				Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
				ContractFn: &invokeArgs,
			},
		},
	}}
}
