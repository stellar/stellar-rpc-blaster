package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	report, err := runVegetaAttack(
		context.Background(),
		nilLogger(),
		config.Config{TargetRPS: 1, Duration: 20 * time.Millisecond},
		server.Client(),
		nil,
		"test",
		"targetRPS=1 tx/s",
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
	require.Equal(t, "test", report.Workload)
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

func TestDefaultMetricsFileNameIncludesSecondPrecisionTimestampAndMode(t *testing.T) {
	name := DefaultMetricsFileName(config.ModeSACTransfer, time.Date(2026, 4, 30, 1, 2, 3, 987654321, time.UTC))
	require.Equal(t, "tx-load-test-metrics-20260430T010203Z-sac-transfer.ndjson", name)
}

func TestWorkloadMetricsReportIncludesStdoutMetrics(t *testing.T) {
	state := newAttackState(2)
	state.submitted = 10
	state.httpErr = 2
	state.queued = 8
	state.tryAgainLater = 1
	state.submitErrors = 3
	state.ambiguous = 4
	state.included = 5
	state.onChainFail = 1
	state.pollErr = 2
	state.errorCodes.inc("TransactionResultCodeTxBadSeq")
	state.submitOpResults.inc("op_bad_auth")
	state.submitDiagnostics.inc("diagnostic")
	state.onChainErrorCodes.inc("TransactionResultCodeTxFailed")
	state.onChainOpResults.inc("op_inner")
	state.onChainDiagnostics.inc("on-chain diagnostic")
	state.vegetaErrors.inc("502 Bad Gateway")
	state.e2eStats.observe(time.Second)
	state.e2eStats.observe(3 * time.Second)
	state.ledgerStats.observeFinality(100, 102)
	state.ledgerStats.observeFinality(100, 103)
	state.ledgerStats.observeTimeout(100, 105)

	metrics := vegeta.Metrics{
		Requests:   10,
		Rate:       9.5,
		Throughput: 8.5,
		Success:    0.8,
		Latencies: vegeta.LatencyMetrics{
			Mean: 2 * time.Second,
			P50:  time.Second,
			P95:  3 * time.Second,
			P99:  4 * time.Second,
			Max:  5 * time.Second,
		},
		BytesIn:     vegeta.ByteMetrics{Total: 100, Mean: 10},
		BytesOut:    vegeta.ByteMetrics{Total: 200, Mean: 20},
		Duration:    6 * time.Second,
		Wait:        7 * time.Second,
		StatusCodes: map[string]int{"200": 8, "502": 2},
	}
	report := newWorkloadMetricsReport("sac-transfer", "targetRPS=60 tx/s", config.Config{TargetRPS: 60, Duration: 5 * time.Minute, RampUp: 20 * time.Second}, 96, 9*time.Minute, state, metrics)

	require.Equal(t, "sac-transfer", report.Workload)
	require.Equal(t, "targetRPS=60 tx/s", report.RateSummary)
	require.Equal(t, "5m0s", report.Duration.String)
	require.Equal(t, "20s", report.RampUp.String)
	require.Equal(t, 60, report.TargetRPS)
	require.Equal(t, 96, report.PollWorkers)
	require.Equal(t, uint64(10), report.Submission.Submitted)
	require.Equal(t, uint64(5), report.OnChain.Included)
	require.Equal(t, 2, report.E2ELatency.Count)
	require.Equal(t, uint64(2), report.E2ELatency.Timeouts)
	require.Equal(t, 2, report.Ledger.FinalityDistance.Count)
	require.Equal(t, uint32(2), report.Ledger.FinalityDistance.P50)
	require.Equal(t, 2, report.Ledger.TransactionsPerLedger.LedgerCount)
	require.Equal(t, uint64(2), report.Ledger.TransactionsPerLedger.Total)
	require.Equal(t, 1, report.Ledger.TimeoutDistance.Count)
	require.Equal(t, uint64(10), report.Vegeta.Requests)
	require.Equal(t, 80.0, report.Vegeta.SuccessPercent)
	require.Equal(t, []countMetric{{Code: "502 Bad Gateway", Count: 1}}, report.Vegeta.Errors)

	records := newFlatMetricsRecords(newBenchmarkMetricsReport(config.Config{Mode: config.ModeSACTransfer, TargetRPS: 60, Duration: 5 * time.Minute, RampUp: 20 * time.Second}, []workloadMetricsReport{report}))
	summary := requireFlatMetricsRecord(t, records, "summary")
	require.Equal(t, "sac-transfer", summary["workload"])
	require.Equal(t, config.ModeSACTransfer, summary["run_mode"])
	require.Equal(t, "targetRPS=60 tx/s", summary["workload_rate_summary"])
	require.Equal(t, "5m0s", summary["workload_duration_string"])
	require.Equal(t, "20s", summary["workload_ramp_up_string"])
	require.Equal(t, 60, summary["workload_target_rps"])
	require.Equal(t, 96, summary["poll_workers"])
	require.Equal(t, uint64(10), summary["submission_submitted"])
	require.Equal(t, uint64(5), summary["on_chain_included"])
	require.Equal(t, 2, summary["e2e_latency_count"])
	require.Equal(t, uint64(2), summary["e2e_latency_timeouts"])
	require.Equal(t, 2, summary["ledger_finality_distance_count"])
	require.Equal(t, uint32(2), summary["ledger_finality_distance_p50"])
	require.Equal(t, 2, summary["ledger_transactions_per_finality_ledger_ledger_count"])
	require.Equal(t, uint64(2), summary["ledger_transactions_per_finality_ledger_total"])
	require.Equal(t, 1, summary["ledger_timeout_distance_count"])
	require.Equal(t, uint64(10), summary["vegeta_requests"])
	require.Equal(t, 80.0, summary["vegeta_success_percent"])
	require.Equal(t, 8, summary["vegeta_status_code_200"])
	require.Equal(t, 2, summary["vegeta_status_code_502"])

	vegetaError := requireFlatMetricsRecord(t, records, "vegeta_error")
	require.Equal(t, "502 Bad Gateway", vegetaError["code"])
	require.Equal(t, int64(1), vegetaError["count"])

	data, err := json.Marshal(records)
	require.NoError(t, err)
	require.NotContains(t, string(data), "\"submission\":")
	require.NotContains(t, string(data), "\"on_chain\":")
	require.NotContains(t, string(data), "\"ledger\":")
	require.NotContains(t, string(data), "\"vegeta\":")
	require.NotContains(t, string(data), "submit_error_breakdown")
	require.NotContains(t, string(data), "submit_error_op_result_breakdown")
	require.NotContains(t, string(data), "submit_diagnostic_breakdown")
	require.NotContains(t, string(data), "on_chain_failure_breakdown")
	require.NotContains(t, string(data), "on_chain_failure_op_result_breakdown")
	require.NotContains(t, string(data), "on_chain_diagnostic_summary")
	require.NotContains(t, string(data), "counts_by_ledger")
	require.NotContains(t, string(data), "attack_progress")
	require.NotContains(t, string(data), "poll_progress")
	require.NotContains(t, string(data), "vegeta_status_code\",")
}

func TestWriteBenchmarkMetricsReportCreatesJSONFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "metrics.ndjson")
	state := newAttackState(1)
	state.submitted = 1
	state.queued = 1
	state.included = 1
	state.vegetaErrors.inc("boom")
	report := newBenchmarkMetricsReport(config.Config{
		Mode:             config.ModeSACTransfer,
		RPCURL:           "https://rpc.example",
		NumberOfAccounts: 8000,
		Duration:         5 * time.Minute,
		RampUp:           20 * time.Second,
		TargetRPS:        60,
		ClassicRPS:       50,
	}, []workloadMetricsReport{newWorkloadMetricsReport("sac-transfer", "targetRPS=60 tx/s", config.Config{TargetRPS: 60, Duration: 5 * time.Minute, RampUp: 20 * time.Second}, 96, 3*time.Minute, state, vegeta.Metrics{Requests: 1, StatusCodes: map[string]int{"200": 1}})})

	require.NoError(t, writeBenchmarkMetricsReport(path, report))
	data := mustReadFile(t, path)
	require.NotContains(t, string(data), "\"workloads\"")
	require.NotContains(t, string(data), "[")
	require.NotContains(t, string(data), "counts_by_ledger")
	require.NotContains(t, string(data), "attack_progress")
	require.NotContains(t, string(data), "poll_progress")
	require.NotContains(t, string(data), "submit_error_breakdown")
	require.NotContains(t, string(data), "submit_error_op_result_breakdown")
	require.NotContains(t, string(data), "submit_diagnostic_breakdown")
	require.NotContains(t, string(data), "on_chain_failure_breakdown")
	require.NotContains(t, string(data), "on_chain_failure_op_result_breakdown")
	require.NotContains(t, string(data), "on_chain_diagnostic_summary")

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	decoded := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		decoded = append(decoded, record)
	}
	require.Equal(t, "summary", decoded[0]["record_type"])
	require.Equal(t, "sac-transfer", decoded[0]["run_mode"])
	require.Equal(t, "https://rpc.example", decoded[0]["run_rpc_url"])
	require.Equal(t, "sac-transfer", decoded[0]["workload"])
	require.Equal(t, float64(1), decoded[0]["vegeta_status_code_200"])
	require.Equal(t, "vegeta_error", decoded[1]["record_type"])
	require.Equal(t, "boom", decoded[1]["code"])
}

func requireFlatMetricsRecord(t *testing.T, records []flatMetricsRecord, recordType string) flatMetricsRecord {
	t.Helper()
	for _, record := range records {
		if record["record_type"] == recordType {
			return record
		}
	}
	t.Fatalf("record type %q not found in %#v", recordType, records)
	return nil
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestPercentileDurationEdges(t *testing.T) {
	values := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}
	require.Zero(t, percentileDuration(nil, 0.5))
	require.Equal(t, time.Second, percentileDuration(values, -1))
	require.Equal(t, 4*time.Second, percentileDuration(values, 1.5))
	require.Equal(t, 2*time.Second, percentileDuration(values, 0.5))
}

// txResultErrorXDR builds a base64-encoded TransactionResult XDR with the
// given outer code and (optionally) inner code. Used to drive the ERROR
// branch of handleSendTransactionEnvelope with realistic result XDRs.
func txResultErrorXDR(t *testing.T, outer xdr.TransactionResultCode, inner *xdr.TransactionResultCode) string {
	t.Helper()
	r := xdr.TransactionResult{}
	switch outer {
	case xdr.TransactionResultCodeTxFeeBumpInnerSuccess, xdr.TransactionResultCodeTxFeeBumpInnerFailed:
		require.NotNil(t, inner)
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
		require.Nil(t, inner)
		r.Result = xdr.TransactionResultResult{Code: outer}
	}
	b64, err := xdr.MarshalBase64(r)
	require.NoError(t, err)
	return b64
}

func TestHandleSendTransactionEnvelopeTracksStatuses(t *testing.T) {
	state := newAttackState(4)
	leases := &fakeLeaseManager{}

	innerBadSeq := xdr.TransactionResultCodeTxBadSeq
	badSeqDirect := txResultErrorXDR(t, xdr.TransactionResultCodeTxBadSeq, nil)
	badSeqWrapped := txResultErrorXDR(t, xdr.TransactionResultCodeTxFeeBumpInnerFailed, &innerBadSeq)
	insufficientFee := txResultErrorXDR(t, xdr.TransactionResultCodeTxInsufficientFee, nil)

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
	// ERROR with non-decodable XDR routes to retryable (default ERROR path).
	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     13,
		Result: protocol.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: "AAAA"},
	}, time.Unix(3, 0), leases))
	require.False(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     14,
		Result: protocol.SendTransactionResponse{Status: "UNKNOWN"},
	}, time.Unix(4, 0), leases))
	// ERROR with a tx_bad_seq result XDR routes to ambiguous (recovery), not
	// retryable, so the cached seq is reloaded from chain instead of looping
	// on the same wrong seq.
	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     15,
		Result: protocol.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: badSeqDirect},
	}, time.Unix(5, 0), leases))
	// Same for tx_fee_bump_inner_failed wrapping tx_bad_seq.
	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     16,
		Result: protocol.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: badSeqWrapped},
	}, time.Unix(6, 0), leases))
	// ERROR with a non-bad_seq decodable XDR (e.g. insufficient_fee) still
	// routes to retryable.
	require.True(t, state.handleSendTransactionEnvelope(sendRespEnvelope{
		ID:     17,
		Result: protocol.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: insufficientFee},
	}, time.Unix(7, 0), leases))

	_, _, queued, tryAgainLater, submitErrors, ambiguous := state.submissionSnapshot()
	require.Equal(t, uint64(1), queued)
	require.Equal(t, uint64(1), tryAgainLater)
	require.Equal(t, uint64(4), submitErrors) // IDs 13, 15, 16, 17.
	require.Equal(t, uint64(1), ambiguous)    // submit-time ambiguous counter only — bad_seq routing uses ReleaseAmbiguous but doesn't bump this counter directly.
	require.Equal(t, []int64{12, 13, 17}, leases.retryableReleases)
	require.Equal(t, []int64{14, 15, 16}, leases.ambiguousReleases)
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
