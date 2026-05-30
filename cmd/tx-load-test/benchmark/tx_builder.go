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

const benchmarkTransactionTimeoutSecs = 20

type sorobanSendTransactionParams struct {
	RPCID             int64
	NetworkPassphrase string
	FeePayerKP        *keypair.Full
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
		BaseFee:              sampleBenchmarkBaseFee(),
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(benchmarkTransactionTimeoutSecs)},
	})
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}
	return buildBenchmarkSendTransactionBody(params.RPCID, params.NetworkPassphrase, params.FeePayerKP, tx, params.Signers...)
}

func buildBenchmarkSendTransactionBody(rpcID int64, networkPassphrase string, feePayerKP *keypair.Full, innerTx *txnbuild.Transaction, signers ...*keypair.Full) ([]byte, error) {
	b64, err := buildBenchmarkEnvelope(networkPassphrase, feePayerKP, innerTx, signers...)
	if err != nil {
		return nil, err
	}
	return marshalSendTransactionBody(rpcID, b64)
}

func buildBenchmarkEnvelope(networkPassphrase string, feePayerKP *keypair.Full, innerTx *txnbuild.Transaction, signers ...*keypair.Full) (string, error) {
	if feePayerKP == nil {
		return "", fmt.Errorf("missing fee payer keypair for benchmark submission")
	}
	if innerTx == nil {
		return "", fmt.Errorf("missing inner transaction for benchmark submission")
	}

	signedInner, err := innerTx.Sign(networkPassphrase, signers...)
	if err != nil {
		return "", fmt.Errorf("sign inner transaction: %w", err)
	}
	feeBump, err := txnbuild.NewFeeBumpTransaction(txnbuild.FeeBumpTransactionParams{
		Inner:      signedInner,
		FeeAccount: feePayerKP.Address(),
		BaseFee:    signedInner.BaseFee(),
	})
	if err != nil {
		return "", fmt.Errorf("build fee-bump transaction: %w", err)
	}
	feeBump, err = feeBump.Sign(networkPassphrase, feePayerKP)
	if err != nil {
		return "", fmt.Errorf("sign fee-bump transaction: %w", err)
	}
	b64, err := feeBump.Base64()
	if err != nil {
		return "", fmt.Errorf("marshal fee-bump transaction: %w", err)
	}
	return b64, nil
}

func marshalSendTransactionBody(rpcID int64, transaction string) ([]byte, error) {
	body, err := json.Marshal(rpcJSONBody{
		JSONRPC: "2.0",
		ID:      rpcID,
		Method:  protocol.SendTransactionMethodName,
		Params:  map[string]string{"transaction": transaction},
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
