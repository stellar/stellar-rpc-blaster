package ledger

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// txResultB64 builds a TransactionResult with the given outer code (and
// optionally a fee-bump inner code) and returns its base64 XDR encoding.
//
// The TransactionResult union has variants that carry payloads: TxSuccess /
// TxFailed (operation results), TxFeeBumpInnerSuccess / TxFeeBumpInnerFailed
// (an inner-result pair). The InnerTransactionResultResult union has the same
// pattern for TxSuccess / TxFailed. This helper sets up empty payload slices
// for those cases so xdr.Marshal does not panic on a missing union member.
func txResultB64(t *testing.T, outer xdr.TransactionResultCode, inner *xdr.TransactionResultCode) string {
	t.Helper()
	r := xdr.TransactionResult{}
	switch outer {
	case xdr.TransactionResultCodeTxSuccess, xdr.TransactionResultCodeTxFailed:
		require.Nil(t, inner, "non-fee-bump outer code does not carry an inner")
		empty := []xdr.OperationResult{}
		r.Result = xdr.TransactionResultResult{Code: outer, Results: &empty}
	case xdr.TransactionResultCodeTxFeeBumpInnerSuccess, xdr.TransactionResultCodeTxFeeBumpInnerFailed:
		require.NotNil(t, inner, "fee-bump-wrapped result requires an inner code")
		innerRR := xdr.InnerTransactionResultResult{Code: *inner}
		switch *inner {
		case xdr.TransactionResultCodeTxSuccess, xdr.TransactionResultCodeTxFailed:
			empty := []xdr.OperationResult{}
			innerRR.Results = &empty
		}
		pair := xdr.InnerTransactionResultPair{
			Result: xdr.InnerTransactionResult{Result: innerRR},
		}
		r.Result = xdr.TransactionResultResult{Code: outer, InnerResultPair: &pair}
	default:
		require.Nil(t, inner, "non-fee-bump outer code does not carry an inner")
		r.Result = xdr.TransactionResultResult{Code: outer}
	}
	b64, err := xdr.MarshalBase64(r)
	require.NoError(t, err)
	return b64
}

func TestIsBadSeqResult_DirectBadSeq(t *testing.T) {
	xdrStr := txResultB64(t, xdr.TransactionResultCodeTxBadSeq, nil)
	require.True(t, IsBadSeqResult(xdrStr))
}

func TestIsBadSeqResult_FeeBumpWrappedBadSeq(t *testing.T) {
	inner := xdr.TransactionResultCodeTxBadSeq
	xdrStr := txResultB64(t, xdr.TransactionResultCodeTxFeeBumpInnerFailed, &inner)
	require.True(t, IsBadSeqResult(xdrStr))
}

func TestIsBadSeqResult_FeeBumpWrappedNotBadSeq(t *testing.T) {
	// fee-bump inner failed for a different reason — must NOT be classified
	// as bad_seq because routing it to ambiguous would over-trigger recovery.
	inner := xdr.TransactionResultCodeTxFailed
	xdrStr := txResultB64(t, xdr.TransactionResultCodeTxFeeBumpInnerFailed, &inner)
	require.False(t, IsBadSeqResult(xdrStr))
}

func TestIsBadSeqResult_OtherTopLevelCodes(t *testing.T) {
	cases := []xdr.TransactionResultCode{
		xdr.TransactionResultCodeTxSuccess,
		xdr.TransactionResultCodeTxFailed,
		xdr.TransactionResultCodeTxInsufficientFee,
		xdr.TransactionResultCodeTxTooLate,
		xdr.TransactionResultCodeTxNoAccount,
	}
	for _, code := range cases {
		t.Run(code.String(), func(t *testing.T) {
			require.False(t, IsBadSeqResult(txResultB64(t, code, nil)))
		})
	}
}

func TestIsBadSeqResult_EmptyAndInvalid(t *testing.T) {
	require.False(t, IsBadSeqResult(""), "empty XDR is not bad_seq")
	require.False(t, IsBadSeqResult("not-base64"), "invalid base64 is not bad_seq")
	require.False(t, IsBadSeqResult("AAAA"), "truncated XDR is not bad_seq")
}

func TestIsBadSeqFromTxResult_NilSafety(t *testing.T) {
	require.False(t, IsBadSeqFromTxResult(nil))
}

func TestDecodeTransactionResultCode_FormatsBoth(t *testing.T) {
	// Plain bad_seq.
	require.Equal(t,
		xdr.TransactionResultCodeTxBadSeq.String(),
		DecodeTransactionResultCode(txResultB64(t, xdr.TransactionResultCodeTxBadSeq, nil)),
	)
	// Fee-bump-wrapped bad_seq formats as "outer (inner: TxBadSeq)".
	inner := xdr.TransactionResultCodeTxBadSeq
	got := DecodeTransactionResultCode(txResultB64(t, xdr.TransactionResultCodeTxFeeBumpInnerFailed, &inner))
	require.Contains(t, got, xdr.TransactionResultCodeTxFeeBumpInnerFailed.String())
	require.Contains(t, got, "inner: "+xdr.TransactionResultCodeTxBadSeq.String())
}

func TestDecodeTransactionResultCode_EmptyAndInvalid(t *testing.T) {
	require.Equal(t, "unknown", DecodeTransactionResultCode(""))
	require.Equal(t, "decode-error", DecodeTransactionResultCode("not-base64"))
}
