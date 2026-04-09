package benchmark

import (
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

func TestPollWorkerCountBounds(t *testing.T) {
	require.Equal(t, 20, pollWorkerCount(1))
	require.Equal(t, 20, pollWorkerCount(100))
	require.Equal(t, 40, pollWorkerCount(200))
	require.Equal(t, 200, pollWorkerCount(2_000))
}

func TestPercentileDurationEdges(t *testing.T) {
	values := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}
	require.Zero(t, percentileDuration(nil, 0.5))
	require.Equal(t, time.Second, percentileDuration(values, -1))
	require.Equal(t, 4*time.Second, percentileDuration(values, 1.5))
	require.Equal(t, 2*time.Second, percentileDuration(values, 0.5))
}

func TestHandleSendTransactionEnvelopeTracksStatuses(t *testing.T) {
	state := newAttackState(4)
	var resetIDs []int64
	resetSeq := func(id int64) {
		resetIDs = append(resetIDs, id)
	}

	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID: 11,
		Result: protocol.SendTransactionResponse{
			Status: "PENDING",
			Hash:   "abc",
		},
	}, time.Unix(1, 0), resetSeq))
	require.Len(t, state.hashes, 1)

	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     12,
		Result: protocol.SendTransactionResponse{Status: "TRY_AGAIN_LATER"},
	}, time.Unix(2, 0), resetSeq))
	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     13,
		Result: protocol.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: "AAAA"},
	}, time.Unix(3, 0), resetSeq))
	require.False(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     14,
		Result: protocol.SendTransactionResponse{Status: "UNKNOWN"},
	}, time.Unix(4, 0), resetSeq))

	_, _, queued, tryAgainLater, submitErrors := state.submissionSnapshot()
	require.Equal(t, uint64(1), queued)
	require.Equal(t, uint64(1), tryAgainLater)
	require.Equal(t, uint64(1), submitErrors)
	require.Equal(t, []int64{12, 13, 14}, resetIDs)
}

func TestProcessAttackResultCountsHTTPFailures(t *testing.T) {
	metrics := vegeta.Metrics{}
	state := newAttackState(1)
	processAttackResult(&vegeta.Result{Code: 500, Error: "boom"}, &metrics, nilLogger(), state, nil, "simple-payment", nil)
	_, httpErr, _, _, _ := state.submissionSnapshot()
	require.Equal(t, uint64(1), httpErr)
	require.Equal(t, uint64(1), metrics.Requests)
}

func TestHandlePollResponseTracksOnChainFailureCodes(t *testing.T) {
	state := newAttackState(1)
	item := pollItem{hash: "abc", submittedAt: time.Now().Add(-time.Second)}
	resp := &protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status: protocol.TransactionStatusFailed,
			DiagnosticEventsXDR: []string{mustDiagnosticEventXDR(t, []xdr.ScVal{
				mustScSymbol("error"),
				mustScString("trying to access an archived contract data entry"),
			}, mustScVec(
				mustScSymbol("Balance"),
				mustScAccountAddress(t, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"),
			))},
		},
	}

	handlePollResponse(nilLogger(), state, item, resp)

	included, onChainFail, pollErr := state.pollSnapshot()
	require.Equal(t, uint64(0), included)
	require.Equal(t, uint64(1), onChainFail)
	require.Equal(t, uint64(0), pollErr)
	require.Equal(t, int64(1), state.onChainErrorCodes.counts["unknown"])
	require.Equal(t, int64(1), state.onChainDiagnostics.counts["trying to access an archived contract data entry | Balance"])
}

func TestSummarizeDiagnosticEventsUsesReadableTokens(t *testing.T) {
	summary := summarizeDiagnosticEvents([]string{
		mustDiagnosticEventXDR(t, []xdr.ScVal{
			mustScSymbol("error"),
			mustScString("trying to access an archived contract data entry"),
		}, mustScVec(
			mustScSymbol("Balance"),
			mustScAccountAddress(t, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"),
		)),
	})
	require.Equal(t, "trying to access an archived contract data entry | Balance", summary)
}

func TestSummarizeDiagnosticEventsIgnoresDynamicAddresses(t *testing.T) {
	firstAddress := keypair.MustRandom().Address()
	secondAddress := keypair.MustRandom().Address()

	first := mustDiagnosticEventXDR(t, []xdr.ScVal{
		mustScSymbol("error"),
		mustScString("trying to access an archived contract data entry"),
	}, mustScVec(
		mustScSymbol("Balance"),
		mustScAccountAddress(t, firstAddress),
	))
	second := mustDiagnosticEventXDR(t, []xdr.ScVal{
		mustScSymbol("error"),
		mustScString("trying to access an archived contract data entry"),
	}, mustScVec(
		mustScSymbol("Balance"),
		mustScAccountAddress(t, secondAddress),
	))

	require.Equal(t, summarizeDiagnosticEvent(first), summarizeDiagnosticEvent(second))
}

func mustDiagnosticEventXDR(t *testing.T, topics []xdr.ScVal, data xdr.ScVal) string {
	t.Helper()
	body, err := xdr.NewContractEventBody(0, xdr.ContractEventV0{Topics: topics, Data: data})
	require.NoError(t, err)

	encoded, err := xdr.MarshalBase64(xdr.DiagnosticEvent{
		InSuccessfulContractCall: false,
		Event: xdr.ContractEvent{
			Type: xdr.ContractEventTypeDiagnostic,
			Body: body,
		},
	})
	require.NoError(t, err)
	return encoded
}

func mustScSymbol(value string) xdr.ScVal {
	sym := xdr.ScSymbol(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
}

func mustScString(value string) xdr.ScVal {
	str := xdr.ScString(value)
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &str}
}

func mustScAccountAddress(t *testing.T, address string) xdr.ScVal {
	t.Helper()
	accountID, err := xdr.AddressToAccountId(address)
	require.NoError(t, err)
	return xdr.ScVal{
		Type: xdr.ScValTypeScvAddress,
		Address: &xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &accountID,
		},
	}
}

func mustScVec(values ...xdr.ScVal) xdr.ScVal {
	vec := xdr.ScVec(values)
	vecRef := &vec
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecRef}
}
