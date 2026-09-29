package benchmark

import (
	"context"
	"fmt"
	"strings"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const readinessBatchSize = 100

func contractInstanceExists(ctx context.Context, rpc interface {
	GetLedgerEntries(context.Context, protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
}, contractID xdr.ContractId) (bool, error) {
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

func fetchLedgerEntriesByKey(
	ctx context.Context,
	rpc interface {
		GetLedgerEntries(context.Context, protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
	},
	keys []string,
) (map[string]protocol.LedgerEntryResult, error) {
	entries := make(map[string]protocol.LedgerEntryResult, len(keys))
	for start := 0; start < len(keys); start += readinessBatchSize {
		end := min(start+readinessBatchSize, len(keys))
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

func fetchTrustlineBalances(
	ctx context.Context,
	st *state.State,
	accounts []*keypair.Full,
) (map[string]map[string]xdr.Int64, error) {
	type keyMeta struct{ account, assetCode string }

	keys := make([]string, 0, len(accounts)*len(st.Assets))
	metas := make([]keyMeta, 0, len(accounts)*len(st.Assets))
	for _, kp := range accounts {
		accountID, err := xdr.AddressToAccountId(kp.Address())
		if err != nil {
			return nil, fmt.Errorf("parse account %s: %w", kp.Address(), err)
		}
		for _, asset := range st.Assets {
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

	entries, err := fetchLedgerEntriesByKey(ctx, st.RPCClient, keys)
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

func fetchOZBalances(ctx context.Context, st *state.State, contractID xdr.ContractId) (map[string]xdr.Int128Parts, error) {
	keys := make([]string, 0, len(st.AccountKPs))
	keyToAddress := make(map[string]string, len(st.AccountKPs))
	for _, kp := range st.AccountKPs {
		lk, err := ozBalanceLedgerKey(contractID, kp.Address())
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

	entries, err := fetchLedgerEntriesByKey(ctx, st.RPCClient, keys)
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

func hasPositiveBalance(balance xdr.Int128Parts) bool {
	return balance.Hi > 0 || (balance.Hi == 0 && balance.Lo > 0)
}

func formatExamples(examples []string) string {
	if len(examples) == 0 {
		return ""
	}
	return strings.Join(examples, "; ")
}

func decodeContractID(contractIDStr string) (xdr.ContractId, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, contractIDStr)
	if err != nil {
		return xdr.ContractId{}, err
	}
	var contractID xdr.ContractId
	copy(contractID[:], raw)
	return contractID, nil
}