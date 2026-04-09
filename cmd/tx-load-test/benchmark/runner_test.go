package benchmark

import (
	"testing"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
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
		TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusFailed},
	}

	handlePollResponse(nilLogger(), state, item, resp)

	included, onChainFail, pollErr := state.pollSnapshot()
	require.Equal(t, uint64(0), included)
	require.Equal(t, uint64(1), onChainFail)
	require.Equal(t, uint64(0), pollErr)
	require.Equal(t, int64(1), state.onChainErrorCodes.counts["unknown"])
}
