package state

import (
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

// isInsufficientFee returns true if errorResultXDR from a SendTransaction
// rejection indicates TxInsufficientFee (surge pricing).
func isInsufficientFee(errorResultXDR string) bool {
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(errorResultXDR, &result); err != nil {
		return false
	}
	return result.Result.Code == xdr.TransactionResultCodeTxInsufficientFee
}

// logTxFailure logs structured details for a failed on-chain transaction:
// the result code decoded from resultXDR, per-operation result codes (for
// TxFailed, which indicates one or more classic ops failed  -- e.g.
// AccountMergeHasSubEntries when trustlines are still attached), and any
// diagnostic events (particularly useful for Soroban contract failures).
func logTxFailure(logger *log.Entry, hash, resultXDR string, diagnosticEventsXDR []string) {
	l := logger.WithField("hash", hash).WithField("resultCode", ledger.DecodeTransactionResultCode(resultXDR))
	l.Error("transaction failed on-chain")
	for i, s := range DecodeOperationResults(resultXDR) {
		l.WithField("opIndex", i).Errorf("op result: %s", s)
	}
	for i, ev := range diagnosticEventsXDR {
		l.WithField("event", i).Errorf("diagnostic event: %s", ev)
	}
}

// DecodeOperationResults extracts per-operation result summaries from a base64
// TransactionResult XDR string, descending into fee-bump inner results when
// the outer code is TxFeeBumpInnerSuccess or TxFeeBumpInnerFailed. Returns
// nil if there are no operation-level results to report (e.g. pre-apply
// rejections, or unwrapped fee-bump outer codes).
//
// Use this for one-off callsites; in hot loops where the same XDR is also
// passed to other decoders, prefer ledger.DecodeTxResult +
// OperationResultsFromTxResult so the XDR is decoded once.
func DecodeOperationResults(resultXDR string) []string {
	result, ok := ledger.DecodeTxResult(resultXDR)
	if !ok {
		return nil
	}
	return OperationResultsFromTxResult(&result)
}

// OperationResultsFromTxResult is the parsed-result variant of
// DecodeOperationResults.
func OperationResultsFromTxResult(result *xdr.TransactionResult) []string {
	if result == nil {
		return nil
	}
	var opResults []xdr.OperationResult
	switch result.Result.Code {
	case xdr.TransactionResultCodeTxSuccess, xdr.TransactionResultCodeTxFailed:
		if rs, ok := result.Result.GetResults(); ok {
			opResults = rs
		}
	case xdr.TransactionResultCodeTxFeeBumpInnerSuccess, xdr.TransactionResultCodeTxFeeBumpInnerFailed:
		if pair, ok := result.Result.GetInnerResultPair(); ok {
			ic := pair.Result.Result.Code
			if ic == xdr.TransactionResultCodeTxSuccess || ic == xdr.TransactionResultCodeTxFailed {
				if rs, ok := pair.Result.Result.GetResults(); ok {
					opResults = rs
				}
			}
		}
	}

	out := make([]string, 0, len(opResults))
	for _, r := range opResults {
		out = append(out, describeOpResult(r))
	}
	return out
}

// describeOpResult converts a single OperationResult to a human-readable
// string. For operations that reached execution (OpInner) it further decodes
// the inner type-specific result code; for pre-execution failures it returns
// the OperationResultCode directly.
func describeOpResult(r xdr.OperationResult) string {
	if r.Code != xdr.OperationResultCodeOpInner {
		return r.Code.String()
	}
	tr, ok := r.GetTr()
	if !ok {
		return "OpInner(no-tr)"
	}
	switch tr.Type {
	case xdr.OperationTypeAccountMerge:
		if m, ok := tr.GetAccountMergeResult(); ok {
			return "AccountMerge:" + m.Code.String()
		}
	case xdr.OperationTypePayment:
		if m, ok := tr.GetPaymentResult(); ok {
			return "Payment:" + m.Code.String()
		}
	case xdr.OperationTypeChangeTrust:
		if m, ok := tr.GetChangeTrustResult(); ok {
			return "ChangeTrust:" + m.Code.String()
		}
	case xdr.OperationTypeCreateAccount:
		if m, ok := tr.GetCreateAccountResult(); ok {
			return "CreateAccount:" + m.Code.String()
		}
	}
	return "OpInner(" + tr.Type.String() + ")"
}

// logSendTransactionRejection logs rich details returned directly by
// sendTransaction for a rejected submission. This is especially useful for
// Soroban pre-apply failures like TxSorobanInvalid.
func logSendTransactionRejection(logger *log.Entry, resp protocol.SendTransactionResponse) {
	l := logger.WithField("resultCode", ledger.DecodeTransactionResultCode(resp.ErrorResultXDR))
	if resp.Hash != "" {
		l = l.WithField("hash", resp.Hash)
	}
	l.Error("transaction rejected during submission")
	for i, s := range DecodeOperationResults(resp.ErrorResultXDR) {
		l.WithField("opIndex", i).Errorf("submission op result: %s", s)
	}
	for i, ev := range resp.DiagnosticEventsXDR {
		l.WithField("event", i).Errorf("submission diagnostic event: %s", ev)
	}
	for i, ev := range resp.DiagnosticEventsJSON {
		l.WithField("event", i).Errorf("submission diagnostic event json: %s", string(ev))
	}
	if len(resp.ErrorResultJSON) > 0 {
		l.Errorf("submission error result json: %s", string(resp.ErrorResultJSON))
	}
}
