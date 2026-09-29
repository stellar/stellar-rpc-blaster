package ledger

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const DefaultBatchSize = 100

type LedgerEntriesClient interface {
	GetLedgerEntries(context.Context, protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
}

func DecodeContractID(contractIDStr string) (xdr.ContractId, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, contractIDStr)
	if err != nil {
		return xdr.ContractId{}, err
	}
	var contractID xdr.ContractId
	copy(contractID[:], raw)
	return contractID, nil
}

func EncodeContractID(contractID xdr.ContractId) (string, error) {
	return strkey.Encode(strkey.VersionByteContract, contractID[:])
}

func ContractInstanceExists(ctx context.Context, rpc LedgerEntriesClient, contractID xdr.ContractId) (bool, error) {
	instanceKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &contractID,
			},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}

	keyB64, err := xdr.MarshalBase64(instanceKey)
	if err != nil {
		return false, fmt.Errorf("marshal contract instance key: %w", err)
	}

	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{keyB64}})
	if err != nil {
		return false, fmt.Errorf("get contract instance entry: %w", err)
	}
	return len(resp.Entries) > 0, nil
}

func FetchLedgerEntriesByKey(
	ctx context.Context,
	rpc LedgerEntriesClient,
	keys []string,
	batchSize int,
) (map[string]protocol.LedgerEntryResult, error) {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	entries := make(map[string]protocol.LedgerEntryResult, len(keys))
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys[start:end]})
		if err != nil {
			return nil, err
		}
		for _, entry := range resp.Entries {
			entries[entry.KeyXDR] = entry
		}
	}
	return entries, nil
}

func FetchTrustlineBalances(
	ctx context.Context,
	rpc LedgerEntriesClient,
	assets []txnbuild.CreditAsset,
	accounts []*keypair.Full,
	batchSize int,
) (map[string]map[string]xdr.Int64, error) {
	type keyMeta struct{ account, assetCode string }

	keys := make([]string, 0, len(accounts)*len(assets))
	metas := make([]keyMeta, 0, len(accounts)*len(assets))
	for _, kp := range accounts {
		accountID, err := xdr.AddressToAccountId(kp.Address())
		if err != nil {
			return nil, fmt.Errorf("parse account %s: %w", kp.Address(), err)
		}
		for _, asset := range assets {
			ax, err := asset.ToXDR()
			if err != nil {
				return nil, fmt.Errorf("asset %s to XDR: %w", asset.GetCode(), err)
			}
			lk := xdr.LedgerKey{
				Type: xdr.LedgerEntryTypeTrustline,
				TrustLine: &xdr.LedgerKeyTrustLine{
					AccountId: accountID,
					Asset: xdr.TrustLineAsset{
						Type:       ax.Type,
						AlphaNum4:  ax.AlphaNum4,
						AlphaNum12: ax.AlphaNum12,
					},
				},
			}
			b64, err := xdr.MarshalBase64(lk)
			if err != nil {
				return nil, fmt.Errorf("marshal trustline key: %w", err)
			}
			keys = append(keys, b64)
			metas = append(metas, keyMeta{kp.Address(), asset.GetCode()})
		}
	}

	entries, err := FetchLedgerEntriesByKey(ctx, rpc, keys, batchSize)
	if err != nil {
		return nil, fmt.Errorf("get trustline entries: %w", err)
	}

	keyToMeta := make(map[string]keyMeta, len(keys))
	for i, key := range keys {
		keyToMeta[key] = metas[i]
	}

	result := make(map[string]map[string]xdr.Int64)
	for key, entry := range entries {
		meta, ok := keyToMeta[key]
		if !ok {
			continue
		}
		var data xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(entry.DataXDR, &data); err != nil {
			continue
		}
		if data.TrustLine == nil {
			continue
		}
		if result[meta.account] == nil {
			result[meta.account] = make(map[string]xdr.Int64)
		}
		result[meta.account][meta.assetCode] = data.TrustLine.Balance
	}
	return result, nil
}

func OZBalanceLedgerKey(contractID xdr.ContractId, accountAddress string) (xdr.LedgerKey, error) {
	accountID, err := xdr.AddressToAccountId(accountAddress)
	if err != nil {
		return xdr.LedgerKey{}, err
	}

	balanceVariant := xdr.ScSymbol("Balance")
	balanceKeyVec := xdr.ScVec{
		{Type: xdr.ScValTypeScvSymbol, Sym: &balanceVariant},
		{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &accountID,
		}},
	}
	balanceKeyRef := &balanceKeyVec

	return xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &contractID,
			},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &balanceKeyRef},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}, nil
}

func FetchOZBalances(
	ctx context.Context,
	rpc LedgerEntriesClient,
	contractID xdr.ContractId,
	accounts []*keypair.Full,
	batchSize int,
) (map[string]xdr.Int128Parts, error) {
	keys := make([]string, 0, len(accounts))
	keyToAddress := make(map[string]string, len(accounts))
	for _, kp := range accounts {
		lk, err := OZBalanceLedgerKey(contractID, kp.Address())
		if err != nil {
			return nil, fmt.Errorf("build OZ balance key for %s: %w", kp.Address(), err)
		}
		b64, err := xdr.MarshalBase64(lk)
		if err != nil {
			return nil, fmt.Errorf("marshal OZ balance key for %s: %w", kp.Address(), err)
		}
		keys = append(keys, b64)
		keyToAddress[b64] = kp.Address()
	}

	entries, err := FetchLedgerEntriesByKey(ctx, rpc, keys, batchSize)
	if err != nil {
		return nil, fmt.Errorf("get OZ balance entries: %w", err)
	}

	balances := make(map[string]xdr.Int128Parts, len(entries))
	for key, entry := range entries {
		address := keyToAddress[key]
		var data xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(entry.DataXDR, &data); err != nil {
			continue
		}
		if data.ContractData == nil {
			continue
		}
		balance, ok := data.ContractData.Val.GetI128()
		if !ok {
			continue
		}
		balances[address] = balance
	}
	return balances, nil
}

func HasPositiveI128(balance xdr.Int128Parts) bool {
	return balance.Hi > 0 || (balance.Hi == 0 && balance.Lo > 0)
}

func DecodeTransactionResultCode(resultXDR string) string {
	if resultXDR == "" {
		return "unknown"
	}
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &result); err != nil {
		return "decode-error"
	}
	outer := result.Result.Code.String()
	if result.Result.Code == xdr.TransactionResultCodeTxFeeBumpInnerFailed {
		if inner, ok := result.Result.GetInnerResultPair(); ok {
			innerCode := inner.Result.Result.Code.String()
			return outer + " (inner: " + innerCode + ")"
		}
	}
	return outer
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
