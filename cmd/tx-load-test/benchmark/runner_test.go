package benchmark

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stretchr/testify/require"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

func TestRunVegetaAttackBuildsTargeterWithAttackContext(t *testing.T) {
	t.Parallel()

	var targeterCtx context.Context
	targeterCtxCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"result":{"status":"TRY_AGAIN_LATER"}}`))
	}))
	defer server.Close()

	err := runVegetaAttack(
		context.Background(),
		nilLogger(),
		config.Config{TargetRPS: 1, Duration: 20 * time.Millisecond},
		server.Client(),
		nil,
		"test",
		func(ctx context.Context) (vegeta.Targeter, error) {
			targeterCtx = ctx
			go func() {
				<-ctx.Done()
				select {
				case targeterCtxCanceled <- struct{}{}:
				default:
				}
			}()
			return func(target *vegeta.Target) error {
				target.Method = http.MethodPost
				target.URL = server.URL
				target.Body = []byte(`{"jsonrpc":"2.0","id":1,"method":"sendTransaction"}`)
				target.Header = http.Header{"Content-Type": []string{"application/json"}}
				return nil
			}, nil
		},
		&fakeLeaseManager{},
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, targeterCtx)
	require.ErrorIs(t, targeterCtx.Err(), context.DeadlineExceeded)

	select {
	case <-targeterCtxCanceled:
	case <-time.After(time.Second):
		t.Fatal("targeter builder did not observe attack context cancellation")
	}
}

func TestPollWorkerCountBounds(t *testing.T) {
	require.Equal(t, 80, pollWorkerCount(1))
	require.Equal(t, 160, pollWorkerCount(100))
	require.Equal(t, 320, pollWorkerCount(200))
	require.Equal(t, 1600, pollWorkerCount(2_000))
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
	leases := &fakeLeaseManager{}

	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID: 11,
		Result: protocol.SendTransactionResponse{
			Status:       "PENDING",
			Hash:         "abc",
			LatestLedger: 100,
		},
	}, time.Unix(1, 0), leases))
	require.Len(t, state.hashes, 1)
	queuedItem := <-state.hashes
	require.Equal(t, "abc", queuedItem.hash)
	require.Equal(t, int64(11), queuedItem.rpcID)
	require.Equal(t, uint32(100), queuedItem.submitLatestLedger)

	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     12,
		Result: protocol.SendTransactionResponse{Status: "TRY_AGAIN_LATER"},
	}, time.Unix(2, 0), leases))
	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     13,
		Result: protocol.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: "AAAA"},
	}, time.Unix(3, 0), leases))
	require.False(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     14,
		Result: protocol.SendTransactionResponse{Status: "UNKNOWN"},
	}, time.Unix(4, 0), leases))

	_, _, queued, tryAgainLater, submitErrors, ambiguous := state.submissionSnapshot()
	require.Equal(t, uint64(1), queued)
	require.Equal(t, uint64(1), tryAgainLater)
	require.Equal(t, uint64(1), submitErrors)
	require.Equal(t, uint64(1), ambiguous)
	require.Equal(t, []int64{12, 13}, leases.retryableReleases)
	require.Equal(t, []int64{14}, leases.ambiguousReleases)
}

func TestHandlePollResponseTracksLedgerMetrics(t *testing.T) {
	state := newAttackState(1)
	item := pollItem{hash: "abc", rpcID: 7, submittedAt: time.Now().Add(-time.Second), submitLatestLedger: 100}
	resp := &protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status: protocol.TransactionStatusSuccess,
			Ledger: 103,
		},
	}

	leases := &fakeLeaseManager{}
	handlePollResponse(nilLogger(), state, item, resp, leases)

	snapshot := state.ledgerStats.snapshot()
	require.Equal(t, []uint32{3}, snapshot.finalityDistances)
	require.Equal(t, map[uint32]uint32{103: 1}, snapshot.txsPerLedger)
	require.Zero(t, snapshot.finalitySkipped)
	require.Equal(t, []int64{7}, leases.consumedReleases)
}

func TestHandlePollResponseTracksFailedLedgerMetrics(t *testing.T) {
	state := newAttackState(1)
	item := pollItem{hash: "abc", rpcID: 8, submittedAt: time.Now().Add(-time.Second), submitLatestLedger: 200}
	resp := &protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status: protocol.TransactionStatusFailed,
			Ledger: 205,
		},
	}

	leases := &fakeLeaseManager{}
	handlePollResponse(nilLogger(), state, item, resp, leases)

	snapshot := state.ledgerStats.snapshot()
	require.Equal(t, []uint32{5}, snapshot.finalityDistances)
	require.Equal(t, map[uint32]uint32{205: 1}, snapshot.txsPerLedger)
	require.Zero(t, snapshot.finalitySkipped)
	require.Equal(t, []int64{8}, leases.consumedReleases)
}

func TestLedgerMetricsSkipInvalidFinalityDistances(t *testing.T) {
	stats := newLedgerMetricStats()
	stats.observeFinality(100, 99)
	stats.observeFinality(0, 101)
	stats.observeFinality(100, 0)

	snapshot := stats.snapshot()
	require.Empty(t, snapshot.finalityDistances)
	require.Equal(t, map[uint32]uint32{99: 1, 101: 1}, snapshot.txsPerLedger)
	require.Equal(t, uint64(3), snapshot.finalitySkipped)
}

func TestLedgerMetricsTrackTimeoutDistanceSeparately(t *testing.T) {
	stats := newLedgerMetricStats()
	stats.observeTimeout(100, 104)
	stats.observeTimeout(100, 99)
	stats.observeTimeout(0, 104)

	snapshot := stats.snapshot()
	require.Equal(t, []uint32{4}, snapshot.timeoutDistances)
	require.Equal(t, uint64(2), snapshot.timeoutSkipped)
	require.Empty(t, snapshot.finalityDistances)
	require.Empty(t, snapshot.txsPerLedger)
}

func TestProcessAttackResultCountsHTTPFailures(t *testing.T) {
	metrics := vegeta.Metrics{}
	state := newAttackState(1)
	leases := &fakeLeaseManager{}
	processAttackResult(&vegeta.Result{Code: 500, Error: "boom", URL: "https://rpc.example#blaster-rpc-id=77"}, &metrics, nilLogger(), state, leases, "simple-payment", nil)
	_, httpErr, _, _, _, ambiguous := state.submissionSnapshot()
	require.Equal(t, uint64(1), httpErr)
	require.Equal(t, uint64(1), ambiguous)
	require.Equal(t, uint64(1), metrics.Requests)
	require.Equal(t, []int64{77}, leases.ambiguousReleases)
}

func TestProcessAttackResultCountsRepeatedVegetaErrors(t *testing.T) {
	metrics := vegeta.Metrics{}
	state := newAttackState(3)
	leases := &fakeLeaseManager{}

	processAttackResult(&vegeta.Result{Code: 503, Error: "503 Service Unavailable", URL: "https://rpc.example#blaster-rpc-id=1"}, &metrics, nilLogger(), state, leases, "simple-payment", nil)
	processAttackResult(&vegeta.Result{Code: 503, Error: "503 Service Unavailable", URL: "https://rpc.example#blaster-rpc-id=2"}, &metrics, nilLogger(), state, leases, "simple-payment", nil)
	processAttackResult(&vegeta.Result{Code: 0, Error: "lease simple-payment source account: context deadline exceeded", URL: "https://rpc.example#blaster-rpc-id=3"}, &metrics, nilLogger(), state, leases, "simple-payment", nil)

	require.Equal(t, []rejectionCountEntry{
		{code: "503 Service Unavailable", count: 2},
		{code: "lease simple-payment source account: context deadline exceeded", count: 1},
	}, state.vegetaErrors.entries())
	require.ElementsMatch(t, []string{
		"503 Service Unavailable",
		"lease simple-payment source account: context deadline exceeded",
	}, metrics.Errors)
}

func TestLogVegetaMetricsIncludesErrorCounts(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New()
	logger.SetLevel(log.InfoLevel)
	logger.SetOutput(&buf)
	logger.DisableColors()
	logger.DisableTimestamp()

	logVegetaMetrics(logger, vegeta.Metrics{
		StatusCodes: map[string]int{"503": 290, "0": 609},
		Errors: []string{
			"503 Service Unavailable",
			"lease simple-payment source account: context deadline exceeded",
		},
	}, []rejectionCountEntry{
		{code: "lease simple-payment source account: context deadline exceeded", count: 609},
		{code: "503 Service Unavailable", count: 290},
	})

	output := buf.String()
	require.Contains(t, output, "error: lease simple-payment source account: context deadline exceeded (609)")
	require.Contains(t, output, "error: 503 Service Unavailable (290)")
	require.False(t, strings.Contains(output, "error: 503 Service Unavailable\"\n"), output)
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

	leases := &fakeLeaseManager{}
	handlePollResponse(nilLogger(), state, item, resp, leases)

	included, onChainFail, pollErr := state.pollSnapshot()
	require.Equal(t, uint64(0), included)
	require.Equal(t, uint64(1), onChainFail)
	require.Equal(t, uint64(0), pollErr)
	require.Equal(t, int64(1), state.onChainErrorCodes.counts["unknown"])
	require.Equal(t, int64(1), state.onChainDiagnostics.counts["trying to access an archived contract data entry | Balance"])
	require.Empty(t, leases.consumedReleases)
}

func TestRequestIDFromResultURL(t *testing.T) {
	require.Equal(t, int64(42), requestIDFromResultURL("https://rpc.example#blaster-rpc-id=42"))
	require.Zero(t, requestIDFromResultURL("https://rpc.example"))
	require.Zero(t, requestIDFromResultURL("://bad-url"))
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
