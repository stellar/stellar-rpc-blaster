package benchmark

import (
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
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
	hashes     chan pollItem
	errorCodes *rejectionCounts
	e2eStats   e2eLatencyStats

	submitted     uint64
	httpErr       uint64
	queued        uint64
	tryAgainLater uint64
	submitErrors  uint64
	included      uint64
	onChainFail   uint64
	pollErr       uint64
}

func newAttackState(maxTx int) *attackState {
	return &attackState{
		hashes:     make(chan pollItem, maxTx),
		errorCodes: newRejectionCounts(),
	}
}

func (s *attackState) resetSequence(resetSeq SequenceResetFunc, rpcID int64) {
	if resetSeq != nil && rpcID > 0 {
		resetSeq(rpcID)
	}
}

func (s *attackState) handleSendTransactionEnvelope(envelope sendRespEnvelope, submittedAt time.Time, resetSeq SequenceResetFunc) bool {
	switch envelope.Result.Status {
	case "PENDING", "DUPLICATE":
		s.hashes <- pollItem{hash: envelope.Result.Hash, rpcID: envelope.ID, submittedAt: submittedAt}
		atomic.AddUint64(&s.queued, 1)
		return true
	case "TRY_AGAIN_LATER":
		atomic.AddUint64(&s.tryAgainLater, 1)
		s.resetSequence(resetSeq, envelope.ID)
		return true
	case "ERROR":
		atomic.AddUint64(&s.submitErrors, 1)
		s.errorCodes.inc(ledger.DecodeTransactionResultCode(envelope.Result.ErrorResultXDR))
		s.resetSequence(resetSeq, envelope.ID)
		return true
	default:
		s.resetSequence(resetSeq, envelope.ID)
		return false
	}
}

func processAttackResult(res *vegeta.Result, metrics *vegeta.Metrics, logger *log.Entry, state *attackState, resetSeq SequenceResetFunc) {
	metrics.Add(res)
	atomic.AddUint64(&state.submitted, 1)

	if res.Error != "" || res.Code < 200 || res.Code >= 300 {
		atomic.AddUint64(&state.httpErr, 1)
		logger.Debugf("HTTP error: code=%d err=%s", res.Code, res.Error)
		return
	}

	var envelope sendRespEnvelope
	if err := json.Unmarshal(res.Body, &envelope); err != nil {
		atomic.AddUint64(&state.httpErr, 1)
		logger.Debugf("parse response body: %v", err)
		return
	}
	if !state.handleSendTransactionEnvelope(envelope, res.Timestamp, resetSeq) {
		atomic.AddUint64(&state.httpErr, 1)
		logger.Debugf("sendTransaction: unknown status %q", envelope.Result.Status)
	}
}

func drainAttackResults(results <-chan *vegeta.Result, metrics *vegeta.Metrics, logger *log.Entry, state *attackState, resetSeq SequenceResetFunc) {
	for res := range results {
		processAttackResult(res, metrics, logger, state, resetSeq)
	}
}

func (s *attackState) submissionSnapshot() (submitted, httpErr, queued, tryAgainLater, submitErrors uint64) {
	return atomic.LoadUint64(&s.submitted),
		atomic.LoadUint64(&s.httpErr),
		atomic.LoadUint64(&s.queued),
		atomic.LoadUint64(&s.tryAgainLater),
		atomic.LoadUint64(&s.submitErrors)
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

func (r *rejectionCounts) log(logger *log.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.counts) == 0 {
		return
	}
	for code, n := range r.counts {
		logger.Infof("  ERROR breakdown: %s=%d", code, n)
	}
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
