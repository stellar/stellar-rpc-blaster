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
	// SorobanDataExt carries the simulator's SorobanTransactionDataExt
	// verbatim. For protocol-23+ autorestore this holds
	// SorobanResourcesExtV0.ArchivedSorobanEntries: the read-write footprint
	// indices core will auto-restore inline at apply time. Dropping it forces
	// apply to fail with invokeHostFunctionEntryArchived even though
	// ResourceFee already prices the inline restoration.
	SorobanDataExt xdr.SorobanTransactionDataExt
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
				Ext:         params.SorobanDataExt,
			},
		},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: params.TxSource.Address(), Sequence: params.Sequence},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              benchmarkInnerBaseFee(params.ResourceFee),
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(benchmarkTransactionTimeoutSecs)},
	})
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}
	return buildBenchmarkSendTransactionBody(params.RPCID, params.NetworkPassphrase, params.FeePayerKP, tx, params.Signers...)
}

// benchmarkInnerBaseFee returns the per-op inclusion fee for a benchmark
// transaction. It samples from the usual [benchmarkBaseFeeMin,
// benchmarkBaseFeeMax] range but raises the floor when the simulator-reported
// resource fee is large (i.e. autorestore or other expensive Soroban work).
//
// Stellar-core enforces a self-induced surge price during sustained
// Soroban-heavy traffic; the inclusion floor scales with how much resource
// work each tx consumes. Sampling a low fill-the-gap inclusion fee (the usual
// [benchmarkBaseFeeMin, benchmarkBaseFeeMax] band, a few hundred stroops) on a
// tx whose resource fee is several million stroops underbids that floor and
// shows up as txINSUFFICIENT_FEE at submit time. This threshold is only
// crossed by expensive Soroban work such as autorestore -- steady-state
// transfers have resource fees ~15k, far below heavyResourceFeeThreshold, so
// the floor does not apply to them and the low fill-the-gap bids stand.
// Boosting the inclusion fee for the heavy case lets the fee-bump's
// totalFee = (numOps+1) * baseFee + resourceFee actually clear the floor.
func benchmarkInnerBaseFee(resourceFee xdr.Int64) int64 {
	baseFee := sampleBenchmarkBaseFee()
	const heavyResourceFeeThreshold xdr.Int64 = 10_000_000
	if resourceFee >= heavyResourceFeeThreshold {
		const heavyResourceBaseFeeFloor int64 = 1_000_000
		if baseFee < heavyResourceBaseFeeFloor {
			baseFee = heavyResourceBaseFeeFloor
		}
	}
	return baseFee
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
