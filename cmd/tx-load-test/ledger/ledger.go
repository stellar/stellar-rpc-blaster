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
	keyB64, err := xdr.MarshalBase64(ContractInstanceLedgerKey(contractID))
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
	holder := xdr.ScAddress{
		Type:      xdr.ScAddressTypeScAddressTypeAccount,
		AccountId: &accountID,
	}
	return ContractBalanceLedgerKey(contractID, holder), nil
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

// DecodeTxResult parses a base64-encoded TransactionResult XDR. Callers that
// need multiple pieces of information from the same XDR (e.g. the formatted
// code string AND a bad_seq classification) should use this and pass the
// parsed value into ResultCodeFromTxResult / IsBadSeqFromTxResult to avoid
// decoding the XDR multiple times in the hot ERROR path.
func DecodeTxResult(resultXDR string) (xdr.TransactionResult, bool) {
	var result xdr.TransactionResult
	if resultXDR == "" {
		return result, false
	}
	if err := xdr.SafeUnmarshalBase64(resultXDR, &result); err != nil {
		return result, false
	}
	return result, true
}

// ResultCodeFromTxResult formats a parsed TransactionResult's outer code,
// descending into the inner code when the outer is TxFeeBumpInnerFailed.
func ResultCodeFromTxResult(result *xdr.TransactionResult) string {
	if result == nil {
		return "unknown"
	}
	outer := result.Result.Code.String()
	if result.Result.Code == xdr.TransactionResultCodeTxFeeBumpInnerFailed {
		if inner, ok := result.Result.GetInnerResultPair(); ok {
			return outer + " (inner: " + inner.Result.Result.Code.String() + ")"
		}
	}
	return outer
}

// IsBadSeqFromTxResult reports whether the parsed result encodes a TxBadSeq
// outcome, either directly or wrapped in TxFeeBumpInnerFailed.
func IsBadSeqFromTxResult(result *xdr.TransactionResult) bool {
	if result == nil {
		return false
	}
	if result.Result.Code == xdr.TransactionResultCodeTxBadSeq {
		return true
	}
	if result.Result.Code == xdr.TransactionResultCodeTxFeeBumpInnerFailed {
		if inner, ok := result.Result.GetInnerResultPair(); ok {
			return inner.Result.Result.Code == xdr.TransactionResultCodeTxBadSeq
		}
	}
	return false
}

// DecodeTransactionResultCode is a string-in / string-out wrapper that decodes
// the XDR and formats the result code in one call. Use this for one-off
// callsites; in hot loops, prefer DecodeTxResult + ResultCodeFromTxResult.
func DecodeTransactionResultCode(resultXDR string) string {
	if resultXDR == "" {
		return "unknown"
	}
	result, ok := DecodeTxResult(resultXDR)
	if !ok {
		return "decode-error"
	}
	return ResultCodeFromTxResult(&result)
}

// IsBadSeqResult is a string-in wrapper for IsBadSeqFromTxResult. Prefer the
// parsed-result variant in hot loops.
func IsBadSeqResult(resultXDR string) bool {
	result, ok := DecodeTxResult(resultXDR)
	if !ok {
		return false
	}
	return IsBadSeqFromTxResult(&result)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
