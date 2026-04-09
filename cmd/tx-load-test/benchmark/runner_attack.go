package benchmark

import (
	"cmp"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	txstate "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// sendRespEnvelope is the minimal JSON-RPC response envelope needed to
// extract the sendTransaction result and JSON-RPC ID from a raw Vegeta
// response body. The ID echoes the value set in the request and is used to
// correlate responses back to source accounts for sequence management.
type sendRespEnvelope struct {
	ID     int64                            `json:"id"`
	Result protocol.SendTransactionResponse `json:"result"`
}

// pollItem pairs a transaction hash with the JSON-RPC ID that produced it.
// The ID is used to identify the source account and correct its sequence
// counter if the transaction is never confirmed on-chain (evicted from the
// mempool).
type pollItem struct {
	hash        string
	rpcID       int64
	submittedAt time.Time
}

type attackState struct {
	hashes             chan pollItem
	errorCodes         *rejectionCounts
	submitOpResults    *rejectionCounts
	submitDiagnostics  *rejectionCounts
	onChainErrorCodes  *rejectionCounts
	onChainOpResults   *rejectionCounts
	onChainDiagnostics *rejectionCounts
	e2eStats           e2eLatencyStats

	submitted     uint64
	httpErr       uint64
	queued        uint64
	tryAgainLater uint64
	submitErrors  uint64
	ambiguous     uint64
	included      uint64
	onChainFail   uint64
	pollErr       uint64
}

func newAttackState(maxTx int) *attackState {
	return &attackState{
		hashes:             make(chan pollItem, maxTx),
		errorCodes:         newRejectionCounts(),
		submitOpResults:    newRejectionCounts(),
		submitDiagnostics:  newRejectionCounts(),
		onChainErrorCodes:  newRejectionCounts(),
		onChainOpResults:   newRejectionCounts(),
		onChainDiagnostics: newRejectionCounts(),
	}
}

func releaseRetryable(accounts accountLeaseManager, requestID int64) {
	if accounts != nil && requestID > 0 {
		accounts.ReleaseRetryable(requestID)
	}
}

func releaseConsumed(accounts accountLeaseManager, requestID int64) {
	if accounts != nil && requestID > 0 {
		accounts.ReleaseConsumed(requestID)
	}
}

func releaseAmbiguous(accounts accountLeaseManager, requestID int64) {
	if accounts != nil && requestID > 0 {
		accounts.ReleaseAmbiguous(requestID)
	}
}

func (s *attackState) handleSendTransactionEnvelope(envelope sendRespEnvelope, submittedAt time.Time, accounts accountLeaseManager) bool {
	switch envelope.Result.Status {
	case "PENDING", "DUPLICATE":
		s.hashes <- pollItem{hash: envelope.Result.Hash, rpcID: envelope.ID, submittedAt: submittedAt}
		atomic.AddUint64(&s.queued, 1)
		return true
	case "TRY_AGAIN_LATER":
		atomic.AddUint64(&s.tryAgainLater, 1)
		releaseRetryable(accounts, envelope.ID)
		return true
	case "ERROR":
		atomic.AddUint64(&s.submitErrors, 1)
		s.errorCodes.inc(ledger.DecodeTransactionResultCode(envelope.Result.ErrorResultXDR))
		for _, opResult := range txstate.DecodeOperationResults(envelope.Result.ErrorResultXDR) {
			s.submitOpResults.inc(opResult)
		}
		if summary := summarizeDiagnosticEvents(envelope.Result.DiagnosticEventsXDR); summary != "" {
			s.submitDiagnostics.inc(summary)
		}
		releaseRetryable(accounts, envelope.ID)
		return true
	default:
		atomic.AddUint64(&s.ambiguous, 1)
		releaseAmbiguous(accounts, envelope.ID)
		return false
	}
}

func processAttackResult(res *vegeta.Result, metrics *vegeta.Metrics, logger *log.Entry, state *attackState, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder) {
	metrics.Add(res)
	atomic.AddUint64(&state.submitted, 1)
	requestID := requestIDFromResultURL(res.URL)

	if res.Error != "" || res.Code < 200 || res.Code >= 300 {
		atomic.AddUint64(&state.httpErr, 1)
		atomic.AddUint64(&state.ambiguous, 1)
		releaseAmbiguous(accounts, requestID)
		if recorder != nil {
			recorder.record(benchmarkTraceRecord{
				Timestamp:    res.Timestamp,
				Workload:     workload,
				Event:        "submit_response",
				HTTPStatus:   int(res.Code),
				Error:        res.Error,
				ResponseBody: string(res.Body),
			})
		}
		logger.Debugf("HTTP error: code=%d err=%s", res.Code, res.Error)
		return
	}

	var envelope sendRespEnvelope
	if err := json.Unmarshal(res.Body, &envelope); err != nil {
		atomic.AddUint64(&state.httpErr, 1)
		atomic.AddUint64(&state.ambiguous, 1)
		releaseAmbiguous(accounts, requestID)
		if recorder != nil {
			recorder.record(benchmarkTraceRecord{
				Timestamp:    res.Timestamp,
				Workload:     workload,
				Event:        "submit_response",
				HTTPStatus:   int(res.Code),
				Error:        err.Error(),
				ResponseBody: string(res.Body),
			})
		}
		logger.Debugf("parse response body: %v", err)
		return
	}
	if recorder != nil {
		resultCode := ""
		if envelope.Result.Status == "ERROR" {
			resultCode = ledger.DecodeTransactionResultCode(envelope.Result.ErrorResultXDR)
		}
		recorder.record(benchmarkTraceRecord{
			Timestamp:         res.Timestamp,
			Workload:          workload,
			Event:             "submit_response",
			RPCID:             envelope.ID,
			Method:            protocol.SendTransactionMethodName,
			HTTPStatus:        int(res.Code),
			TransactionStatus: envelope.Result.Status,
			ResultCode:        resultCode,
			Hash:              envelope.Result.Hash,
			ResponseBody:      string(res.Body),
		})
	}
	if !state.handleSendTransactionEnvelope(envelope, res.Timestamp, accounts) {
		atomic.AddUint64(&state.httpErr, 1)
		logger.Debugf("sendTransaction: unknown status %q", envelope.Result.Status)
	}
}

func drainAttackResults(results <-chan *vegeta.Result, metrics *vegeta.Metrics, logger *log.Entry, state *attackState, accounts accountLeaseManager, workload string, recorder *benchmarkTraceRecorder) {
	for res := range results {
		processAttackResult(res, metrics, logger, state, accounts, workload, recorder)
	}
}

func (s *attackState) submissionSnapshot() (submitted, httpErr, queued, tryAgainLater, submitErrors, ambiguous uint64) {
	return atomic.LoadUint64(&s.submitted),
		atomic.LoadUint64(&s.httpErr),
		atomic.LoadUint64(&s.queued),
		atomic.LoadUint64(&s.tryAgainLater),
		atomic.LoadUint64(&s.submitErrors),
		atomic.LoadUint64(&s.ambiguous)
}

func requestIDFromResultURL(rawURL string) int64 {
	if rawURL == "" {
		return 0
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	if !strings.HasPrefix(parsed.Fragment, requestIDURLFragmentPrefix) {
		return 0
	}
	requestID, err := strconv.ParseInt(strings.TrimPrefix(parsed.Fragment, requestIDURLFragmentPrefix), 10, 64)
	if err != nil {
		return 0
	}
	return requestID
}

func (s *attackState) pollSnapshot() (included, onChainFail, pollErr uint64) {
	return atomic.LoadUint64(&s.included),
		atomic.LoadUint64(&s.onChainFail),
		atomic.LoadUint64(&s.pollErr)
}

type e2eLatencyStats struct {
	mu        sync.Mutex
	latencies []time.Duration
}

func (s *e2eLatencyStats) observe(d time.Duration) {
	s.mu.Lock()
	s.latencies = append(s.latencies, d)
	s.mu.Unlock()
}

func (s *e2eLatencyStats) snapshot() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.latencies))
	copy(out, s.latencies)
	return out
}

// rejectionCounts tracks how many times each TransactionResultCode was
// returned in an ERROR sendTransaction response.
type rejectionCounts struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newRejectionCounts() *rejectionCounts {
	return &rejectionCounts{counts: make(map[string]int64)}
}

func (r *rejectionCounts) inc(code string) {
	r.mu.Lock()
	r.counts[code]++
	r.mu.Unlock()
}

func (r *rejectionCounts) log(logger *log.Entry, prefix string) {
	for _, entry := range r.entries() {
		logger.Infof("  %s: %s=%d", prefix, entry.code, entry.count)
	}
}

type rejectionCountEntry struct {
	code  string
	count int64
}

func (r *rejectionCounts) entries() []rejectionCountEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.counts) == 0 {
		return nil
	}
	entries := make([]rejectionCountEntry, 0, len(r.counts))
	for code, count := range r.counts {
		entries = append(entries, rejectionCountEntry{code: code, count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return cmp.Less(entries[i].code, entries[j].code)
	})
	return entries
}

func logE2ELatencies(logger *log.Entry, latencies []time.Duration, timeouts uint64) {
	if len(latencies) == 0 {
		logger.Infof("e2e latency (submit start -> terminal poll)  count=0 timeouts=%d", timeouts)
		return
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var total time.Duration
	for _, d := range latencies {
		total += d
	}
	mean := total / time.Duration(len(latencies))
	p50 := percentileDuration(latencies, 0.50)
	p95 := percentileDuration(latencies, 0.95)
	p99 := percentileDuration(latencies, 0.99)
	max := latencies[len(latencies)-1]
	logger.Infof("e2e latency (submit start -> terminal poll)  count=%d mean=%s p50=%s p95=%s p99=%s max=%s timeouts=%d",
		len(latencies), mean, p50, p95, p99, max, timeouts)
}

func logVegetaMetrics(logger *log.Entry, metrics vegeta.Metrics) {
	logger.Info("--- vegeta metrics ---")
	logger.Infof("requests=%d  rate=%.2f req/s  throughput=%.2f req/s  success=%.2f%%",
		metrics.Requests, metrics.Rate, metrics.Throughput, metrics.Success*100)
	logger.Infof("latency  mean=%s  p50=%s  p95=%s  p99=%s  max=%s",
		metrics.Latencies.Mean, metrics.Latencies.P50,
		metrics.Latencies.P95, metrics.Latencies.P99, metrics.Latencies.Max)
	logger.Infof("bytes_in  total=%d  mean=%.0f  |  bytes_out  total=%d  mean=%.0f",
		metrics.BytesIn.Total, metrics.BytesIn.Mean,
		metrics.BytesOut.Total, metrics.BytesOut.Mean)
	logger.Infof("duration=%s  wait=%s", metrics.Duration, metrics.Wait)
	if len(metrics.StatusCodes) > 0 {
		for code, count := range metrics.StatusCodes {
			logger.Infof("  HTTP %s: %d", code, count)
		}
	}
	if len(metrics.Errors) > 0 {
		for _, e := range metrics.Errors {
			logger.Infof("  error: %s", e)
		}
	}
}

func percentileDuration(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}
