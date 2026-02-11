package seed

import (
	"context"

	"github.com/pkg/errors"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

func SeedLedgerKeys(
	ctx context.Context,
	logger *log.Entry,
	rpcClient *rpcclient.Client,
	writer *SeedWriter,
	parameters PreseedParameters,
) error {
	if err := writer.StartArray("ledger_keys"); err != nil {
		return errors.Wrap(err, "failed to start ledger_keys array")
	}
	var endArrayErr error
	defer func() {
		if e := writer.EndArray(); e != nil && endArrayErr == nil {
			endArrayErr = e
		}
	}()

	var cursor string
	for {
		req := util.MakeGetTransactionsRequest(parameters.Range.First, cursor)

		txsResponse, err := rpcClient.GetTransactions(ctx, req)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch transaction data from ledger %d, cursor %s",
				parameters.Range.First, cursor)
		}

		if len(txsResponse.Transactions) == 0 {
			break
		}
		cursor = txsResponse.Cursor

		for _, tx := range txsResponse.Transactions {
			if tx.Ledger > parameters.Range.Last {
				return nil
			}
			txResultMeta := tx.TransactionDetails.ResultMetaXDR
			if txResultMeta == "" {
				continue
			}

			keys, err := getKeysFromTxResultMeta(logger, txResultMeta)
			if err != nil {
				// skip if one bad tx
				logger.Errorf("failed to extract ledger keys from transaction result meta XDR for tx %s: %v",
					tx.TransactionDetails.TransactionHash, err)
				continue
			}
			for _, key := range keys {
				switch key.Type {
				case xdr.LedgerEntryTypeAccount,
					xdr.LedgerEntryTypeTrustline,
					xdr.LedgerEntryTypeContractCode,
					xdr.LedgerEntryTypeContractData:
				default:
					continue
				}

				keyXDR, err := xdr.MarshalBase64(key)
				if err != nil {
					logger.Errorf("failed to marshal ledger key to XDR for tx %s: %v",
						tx.TransactionDetails.TransactionHash, err)
					continue
				}
				if err := writer.WriteItem(keyXDR); err != nil {
					return errors.Wrap(err, "failed to write ledger key item")
				}
			}
		}
	}
	return endArrayErr
}

func getKeysFromTxResultMeta(logger *log.Entry, resultMetaXDR string) ([]xdr.LedgerKey, error) {
	var meta xdr.TransactionMeta
	if err := xdr.SafeUnmarshalBase64(resultMetaXDR, &meta); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal transaction result meta XDR")
	}

	changes, ok := getLedgerEntryChangesFromMeta(meta)
	if !ok {
		return nil, errors.New("couldn't get ledger entry changes")
	}

	out := make([]xdr.LedgerKey, 0, 64)
	for _, change := range changes {
		key, err := change.LedgerKey()
		if err != nil {
			logger.Errorf("failed to get ledger key from change: %v", err)
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

func getLedgerEntryChangesFromMeta(meta xdr.TransactionMeta) ([]xdr.LedgerEntryChange, bool) {
	var changes []xdr.LedgerEntryChange
	switch meta.V {
	case 1:
		changes = append(changes, meta.MustV1().TxChanges...)
		for _, opMeta := range meta.MustV1().Operations {
			changes = append(changes, opMeta.Changes...)
		}
	case 2:
		changes = append(changes, meta.MustV2().TxChangesBefore...)
		for _, opMeta := range meta.MustV2().Operations {
			changes = append(changes, opMeta.Changes...)
		}
		changes = append(changes, meta.MustV2().TxChangesAfter...)
	case 3:
		changes = append(changes, meta.MustV3().TxChangesBefore...)
		for _, op := range meta.MustV3().Operations {
			changes = append(changes, op.Changes...)
		}
		changes = append(changes, meta.MustV3().TxChangesAfter...)
	case 4:
		changes = append(changes, meta.MustV4().TxChangesBefore...)
		for _, op := range meta.MustV4().Operations {
			changes = append(changes, op.Changes...)
		}
		changes = append(changes, meta.MustV4().TxChangesAfter...)
	default:
		return nil, false
	}
	return changes, true
}
