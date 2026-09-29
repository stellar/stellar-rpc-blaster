package benchmark

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const requestIDURLFragmentPrefix = "blaster-rpc-id="

type sorobanSendTransactionParams struct {
	RPCID             int64
	NetworkPassphrase string
	TxSource          *keypair.Full
	Sequence          int64
	Signers           []*keypair.Full
	OpSourceAccount   string
	InvokeArgs        xdr.InvokeContractArgs
	AuthEntries       []xdr.SorobanAuthorizationEntry
	Resources         xdr.SorobanResources
	ResourceFee       xdr.Int64
}

func buildSorobanSendTransactionBody(params sorobanSendTransactionParams) ([]byte, error) {
	op := txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &params.InvokeArgs,
		},
		Auth:          params.AuthEntries,
		SourceAccount: params.OpSourceAccount,
		Ext: xdr.TransactionExt{
			V: 1,
			SorobanData: &xdr.SorobanTransactionData{
				Resources:   params.Resources,
				ResourceFee: params.ResourceFee,
			},
		},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: params.TxSource.Address(), Sequence: params.Sequence},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              benchmarkBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
	})
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}
	tx, err = tx.Sign(params.NetworkPassphrase, params.Signers...)
	if err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)
	}
	b64, err := tx.Base64()
	if err != nil {
		return nil, fmt.Errorf("marshal transaction: %w", err)
	}

	body, err := json.Marshal(rpcJSONBody{
		JSONRPC: "2.0",
		ID:      params.RPCID,
		Method:  protocol.SendTransactionMethodName,
		Params:  map[string]string{"transaction": b64},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal JSON-RPC body: %w", err)
	}
	return body, nil
}

func populateJSONRPCTarget(t *vegeta.Target, rpcURL string, body []byte, requestID int64) {
	targetURL := rpcURL
	if requestID > 0 {
		parsedURL, err := url.Parse(rpcURL)
		if err == nil {
			parsedURL.Fragment = requestIDURLFragmentPrefix + strconv.FormatInt(requestID, 10)
			targetURL = parsedURL.String()
		}
	}
	t.Method = http.MethodPost
	t.URL = targetURL
	t.Body = body
	t.Header = http.Header{"Content-Type": {"application/json"}}
}
