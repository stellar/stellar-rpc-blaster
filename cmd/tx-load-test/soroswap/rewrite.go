package soroswap

import "github.com/stellar/go-stellar-sdk/xdr"

func RewriteInvokeContractAccount(invokeArgs xdr.InvokeContractArgs, oldAddress, newAddress string) (xdr.InvokeContractArgs, error) {
	args := make(xdr.ScVec, len(invokeArgs.Args))
	for i, arg := range invokeArgs.Args {
		rewritten, err := RewriteScValAccount(arg, oldAddress, newAddress)
		if err != nil {
			return xdr.InvokeContractArgs{}, err
		}
		args[i] = rewritten
	}
	invokeArgs.Args = args
	return invokeArgs, nil
}

func RewriteSorobanAuthEntriesAccount(entries []xdr.SorobanAuthorizationEntry, oldAddress, newAddress string) ([]xdr.SorobanAuthorizationEntry, error) {
	rewritten := make([]xdr.SorobanAuthorizationEntry, len(entries))
	for i, entry := range entries {
		invocation, err := rewriteAuthorizedInvocationAccount(entry.RootInvocation, oldAddress, newAddress)
		if err != nil {
			return nil, err
		}
		entry.RootInvocation = invocation
		rewritten[i] = entry
	}
	return rewritten, nil
}

func RewriteFootprintAccount(footprint xdr.LedgerFootprint, oldAddress, newAddress string) (xdr.LedgerFootprint, error) {
	readOnly := make([]xdr.LedgerKey, len(footprint.ReadOnly))
	for i, key := range footprint.ReadOnly {
		rewritten, err := rewriteLedgerKeyAccount(key, oldAddress, newAddress)
		if err != nil {
			return xdr.LedgerFootprint{}, err
		}
		readOnly[i] = rewritten
	}
	readWrite := make([]xdr.LedgerKey, len(footprint.ReadWrite))
	for i, key := range footprint.ReadWrite {
		rewritten, err := rewriteLedgerKeyAccount(key, oldAddress, newAddress)
		if err != nil {
			return xdr.LedgerFootprint{}, err
		}
		readWrite[i] = rewritten
	}
	return xdr.LedgerFootprint{ReadOnly: readOnly, ReadWrite: readWrite}, nil
}

func RewriteScValAccount(value xdr.ScVal, oldAddress, newAddress string) (xdr.ScVal, error) {
	switch value.Type {
	case xdr.ScValTypeScvAddress:
		if value.Address == nil || value.Address.Type != xdr.ScAddressTypeScAddressTypeAccount || value.Address.AccountId == nil {
			return value, nil
		}
		if value.Address.AccountId.Address() != oldAddress {
			return value, nil
		}
		accountID, err := xdr.AddressToAccountId(newAddress)
		if err != nil {
			return xdr.ScVal{}, err
		}
		value.Address = &xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &accountID}
		return value, nil
	case xdr.ScValTypeScvVec:
		if value.Vec == nil || *value.Vec == nil {
			return value, nil
		}
		vec := **value.Vec
		rewrittenVec := make(xdr.ScVec, len(vec))
		for i, entry := range vec {
			rewritten, err := RewriteScValAccount(entry, oldAddress, newAddress)
			if err != nil {
				return xdr.ScVal{}, err
			}
			rewrittenVec[i] = rewritten
		}
		vecRef := &rewrittenVec
		value.Vec = &vecRef
		return value, nil
	case xdr.ScValTypeScvMap:
		if value.Map == nil || *value.Map == nil {
			return value, nil
		}
		m := **value.Map
		rewrittenMap := make(xdr.ScMap, len(m))
		for i, entry := range m {
			key, err := RewriteScValAccount(entry.Key, oldAddress, newAddress)
			if err != nil {
				return xdr.ScVal{}, err
			}
			val, err := RewriteScValAccount(entry.Val, oldAddress, newAddress)
			if err != nil {
				return xdr.ScVal{}, err
			}
			rewrittenMap[i] = xdr.ScMapEntry{Key: key, Val: val}
		}
		mapRef := &rewrittenMap
		value.Map = &mapRef
		return value, nil
	default:
		return value, nil
	}
}

func rewriteAuthorizedInvocationAccount(invocation xdr.SorobanAuthorizedInvocation, oldAddress, newAddress string) (xdr.SorobanAuthorizedInvocation, error) {
	fn, err := rewriteAuthorizedFunctionAccount(invocation.Function, oldAddress, newAddress)
	if err != nil {
		return xdr.SorobanAuthorizedInvocation{}, err
	}
	subs := make([]xdr.SorobanAuthorizedInvocation, len(invocation.SubInvocations))
	for i, sub := range invocation.SubInvocations {
		rewritten, err := rewriteAuthorizedInvocationAccount(sub, oldAddress, newAddress)
		if err != nil {
			return xdr.SorobanAuthorizedInvocation{}, err
		}
		subs[i] = rewritten
	}
	invocation.Function = fn
	invocation.SubInvocations = subs
	return invocation, nil
}

func rewriteAuthorizedFunctionAccount(fn xdr.SorobanAuthorizedFunction, oldAddress, newAddress string) (xdr.SorobanAuthorizedFunction, error) {
	if fn.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn.ContractFn == nil {
		return fn, nil
	}
	rewritten, err := RewriteInvokeContractAccount(*fn.ContractFn, oldAddress, newAddress)
	if err != nil {
		return xdr.SorobanAuthorizedFunction{}, err
	}
	fn.ContractFn = &rewritten
	return fn, nil
}

func rewriteLedgerKeyAccount(key xdr.LedgerKey, oldAddress, newAddress string) (xdr.LedgerKey, error) {
	switch key.Type {
	case xdr.LedgerEntryTypeAccount:
		if key.Account != nil && key.Account.AccountId.Address() == oldAddress {
			accountID, err := xdr.AddressToAccountId(newAddress)
			if err != nil {
				return xdr.LedgerKey{}, err
			}
			key.Account.AccountId = accountID
		}
	case xdr.LedgerEntryTypeTrustline:
		if key.TrustLine != nil && key.TrustLine.AccountId.Address() == oldAddress {
			accountID, err := xdr.AddressToAccountId(newAddress)
			if err != nil {
				return xdr.LedgerKey{}, err
			}
			key.TrustLine.AccountId = accountID
		}
	case xdr.LedgerEntryTypeContractData:
		if key.ContractData != nil {
			rewritten, err := RewriteScValAccount(key.ContractData.Key, oldAddress, newAddress)
			if err != nil {
				return xdr.LedgerKey{}, err
			}
			key.ContractData.Key = rewritten
		}
	}
	return key, nil
}
