package benchmark

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/internal/run/engine"
)

// pollWorkerCount returns the number of goroutines for polling getTransaction
// to confirm on-chain inclusion.  It scales with the target RPS so the drain
// phase after the attack completes in reasonable time.
func pollWorkerCount(targetRPS int) int {
	return max(20, min(targetRPS/5, 200))
}

// pollTimeout is the per-transaction deadline for polling getTransaction to a
// terminal state.  Five ledgers at ~5 s each gives plenty of margin.
const pollTimeout = 30 * time.Second

// sendRespEnvelope is the minimal JSON-RPC response envelope needed to
// extract the sendTransaction result and JSON-RPC ID from a raw Vegeta
// response body.  The ID echoes the value set in the request and is used to
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

// decodeResultCode unmarshals the base64 ErrorResultXDR from a
// sendTransaction ERROR response and returns the TransactionResultCode as a
// human-readable string (e.g. "TransactionResultCodeTxBadSeq").  For fee-bump
// transactions that fail with TxFeeBumpInnerFailed the inner code is appended.
// Returns "unknown" or "decode-error" on any decode failure so it is always
// safe to log.
func decodeResultCode(errorResultXDR string) string {
	if errorResultXDR == "" {
		return "unknown (no XDR)"
	}
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(errorResultXDR, &result); err != nil {
		return "decode-error"
	}
	outer := result.Result.Code.String()
	if result.Result.Code == xdr.TransactionResultCodeTxFeeBumpInnerFailed {
		if inner, ok := result.Result.GetInnerResultPair(); ok {
			innerCode := inner.Result.Result.Code.String()
			return outer + " (inner: " + innerCode + ")"
		}
	}
	return outer
}

// runVegetaAttack is the shared Vegeta harness used by all benchmark modes.
// It constructs a RampToConstantPacer and drives the attack for cfg.Duration.
//
// After the attack finishes, a pool of poll worker goroutines drains the
// accepted-transaction hashes by calling getTransaction until each reaches a
// terminal state (SUCCESS or FAILED).  This gives a complete picture of
// on-chain outcomes, not just submission acceptance.
func runVegetaAttack(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	httpClient *http.Client,
	rpc *rpcclient.Client,
	label string,
	targeter vegeta.Targeter,
	resetSeq SequenceResetFunc,
) error {
	pacer := engine.RampToConstantPacer{
		StartRPS:      1,
		MaxRPS:        cfg.TargetRPS,
		RampDuration:  cfg.RampUp,
		TotalDuration: cfg.Duration,
	}

	attackCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	// hashes receives every PENDING/DUPLICATE submission so the poll workers
	// can confirm on-chain inclusion.  Each item carries both the tx hash and
	// the JSON-RPC ID so poll workers can correct the sequence counter if the
	// tx is never confirmed (mempool eviction).  Buffer is sized to hold the
	// maximum number of transactions over the full duration.
	maxTx := cfg.TargetRPS*int(cfg.Duration.Seconds()) + 1000
	hashes := make(chan pollItem, maxTx)

	// Attack-phase counters.
	var (
		submitted     uint64 // total vegeta results received
		httpErr       uint64 // HTTP / network-level error
		queued        uint64 // PENDING or DUPLICATE  -- sent for inclusion polling
		tryAgainLater uint64 // TRY_AGAIN_LATER  -- core queue full
		submitErrors  uint64 // ERROR  -- transaction rejected by stellar-core
	)
	errorCodes := newRejectionCounts()

	// Inclusion-phase counters (updated by poll workers).
	var (
		included    uint64 // on-chain SUCCESS
		onChainFail uint64 // on-chain FAILED
		pollErr     uint64 // error returned by PollTransaction
	)
	var e2eStats e2eLatencyStats

	// Start pool of poll workers.  They read items until the channel is
	// closed after the attack, then exit.  When a poll times out (the tx was
	// likely evicted from the mempool), the worker calls resetSeq to correct
	// the source account's sequence counter  -- without this, the account would
	// generate BadSeq errors on every subsequent use.
	numPollWorkers := pollWorkerCount(cfg.TargetRPS)
	logger.Infof("starting %d poll workers", numPollWorkers)
	var pollWg sync.WaitGroup
	for range numPollWorkers {
		pollWg.Add(1)
		go func() {
			defer pollWg.Done()
			for item := range hashes {
				pollCtx, pollCancel := context.WithTimeout(ctx, pollTimeout)
				resp, err := rpc.PollTransaction(pollCtx, item.hash)
				pollCancel()
				if err != nil {
					atomic.AddUint64(&pollErr, 1)
					logger.WithError(err).Debugf("poll failed: hash=%s", item.hash)
					// The tx was likely evicted from the mempool; its
					// sequence was never consumed on-ledger.  Decrement
					// the counter so subsequent txs for this account
					// use the correct sequence.
					if resetSeq != nil && item.rpcID > 0 {
						resetSeq(item.rpcID)
					}
					continue
				}
				switch resp.Status {
				case protocol.TransactionStatusSuccess:
					atomic.AddUint64(&included, 1)
					e2eStats.observe(time.Since(item.submittedAt))
				case protocol.TransactionStatusFailed:
					atomic.AddUint64(&onChainFail, 1)
					e2eStats.observe(time.Since(item.submittedAt))
					code := decodeResultCode(resp.ResultXDR)
					entry := logger.WithField("hash", item.hash).WithField("resultCode", code)
					entry.Debug("on-chain failure")
					for i, ev := range resp.DiagnosticEventsXDR {
						entry.Debugf("diagnostic event[%d]: %s", i, ev)
					}
				}
			}
		}()
	}

	// Vegeta metrics aggregator  -- collects latency percentiles, achieved
	// rate, throughput, success ratio, status codes, and byte counts.
	var metrics vegeta.Metrics

	attacker := vegeta.NewAttacker(vegeta.Client(httpClient))
	results := attacker.Attack(targeter, pacer, cfg.Duration, label)

loop:
	for {
		select {
		case <-attackCtx.Done():
			attacker.Stop()
			break loop
		case res, ok := <-results:
			if !ok {
				break loop
			}
			metrics.Add(res)
			atomic.AddUint64(&submitted, 1)

			if res.Error != "" || res.Code < 200 || res.Code >= 300 {
				atomic.AddUint64(&httpErr, 1)
				logger.Debugf("HTTP error: code=%d err=%s", res.Code, res.Error)
				continue
			}

			// Parse the JSON-RPC envelope to get sendTransaction status + hash.
			var envelope sendRespEnvelope
			if err := json.Unmarshal(res.Body, &envelope); err != nil {
				atomic.AddUint64(&httpErr, 1)
				logger.Debugf("parse response body: %v", err)
				continue
			}

			switch envelope.Result.Status {
			case "PENDING", "DUPLICATE":
				// Accepted  -- queue for on-chain confirmation.
				hashes <- pollItem{hash: envelope.Result.Hash, rpcID: envelope.ID, submittedAt: res.Timestamp}
				atomic.AddUint64(&queued, 1)
			case "TRY_AGAIN_LATER":
				// Core queue is full; the transaction never entered the
				// pending set so its sequence number was NOT consumed.
				atomic.AddUint64(&tryAgainLater, 1)
				if resetSeq != nil && envelope.ID > 0 {
					resetSeq(envelope.ID)
				}
			case "ERROR":
				// Transaction was rejected pre-apply; sequence NOT consumed.
				atomic.AddUint64(&submitErrors, 1)
				errorCodes.inc(decodeResultCode(envelope.Result.ErrorResultXDR))
				if resetSeq != nil && envelope.ID > 0 {
					resetSeq(envelope.ID)
				}
			default:
				atomic.AddUint64(&httpErr, 1)
				logger.Debugf("sendTransaction: unknown status %q", envelope.Result.Status)
				if resetSeq != nil && envelope.ID > 0 {
					resetSeq(envelope.ID)
				}
			}
		}
	}

	// Drain any remaining buffered results from the attacker so submitted
	// transactions are not silently lost. Vegeta's channel may still have
	// items queued between attackCtx cancellation and the channel closing.
	for res := range results {
		metrics.Add(res)
		atomic.AddUint64(&submitted, 1)
		if res.Error != "" || res.Code < 200 || res.Code >= 300 {
			atomic.AddUint64(&httpErr, 1)
			continue
		}
		var envelope sendRespEnvelope
		if err := json.Unmarshal(res.Body, &envelope); err != nil {
			atomic.AddUint64(&httpErr, 1)
			continue
		}
		switch envelope.Result.Status {
		case "PENDING", "DUPLICATE":
			hashes <- pollItem{hash: envelope.Result.Hash, rpcID: envelope.ID, submittedAt: res.Timestamp}
			atomic.AddUint64(&queued, 1)
		case "TRY_AGAIN_LATER":
			atomic.AddUint64(&tryAgainLater, 1)
			if resetSeq != nil && envelope.ID > 0 {
				resetSeq(envelope.ID)
			}
		case "ERROR":
			atomic.AddUint64(&submitErrors, 1)
			errorCodes.inc(decodeResultCode(envelope.Result.ErrorResultXDR))
			if resetSeq != nil && envelope.ID > 0 {
				resetSeq(envelope.ID)
			}
		default:
			if resetSeq != nil && envelope.ID > 0 {
				resetSeq(envelope.ID)
			}
		}
	}

	// Attack done.  Close the hash channel so poll workers drain and exit.
	close(hashes)
	metrics.Close()

	logger.Infof("attack complete  -- submitted=%d httpErr=%d queued=%d tryAgainLater=%d submitErrors=%d  -- waiting for poll workers",
		submitted, httpErr, queued, tryAgainLater, submitErrors)
	if submitErrors > 0 {
		errorCodes.log(logger)
	}

	// Wait for poll workers with a bounded timeout.  The pessimistic upper
	// bound uses the per-tx pollTimeout, which massively overestimates because
	// most txs confirm in 1-3 s; only evicted ones take the full timeout.
	// Use 3 s average per batch as the baseline instead.
	pendingPolls := atomic.LoadUint64(&queued)
	batchesNeeded := (pendingPolls + uint64(numPollWorkers) - 1) / uint64(numPollWorkers)
	const estimatedAvgPoll = 3 * time.Second
	drainTimeout := time.Duration(batchesNeeded)*estimatedAvgPoll + 2*time.Minute
	if drainTimeout < 3*time.Minute {
		drainTimeout = 3 * time.Minute
	}
	logger.Infof("waiting up to %s for %d poll workers to drain %d queued txs", drainTimeout, numPollWorkers, pendingPolls)

	pollDone := make(chan struct{})
	go func() {
		pollWg.Wait()
		close(pollDone)
	}()

	// Progress ticker  -- log poll drain status every 10 s so the operator can
	// see forward progress (or lack thereof).
	progressTicker := time.NewTicker(10 * time.Second)
	defer progressTicker.Stop()
	drainDeadline := time.After(drainTimeout)

drainLoop:
	for {
		select {
		case <-pollDone:
			break drainLoop
		case <-drainDeadline:
			logger.Warnf("poll workers still running after %s -- abandoning", drainTimeout)
			break drainLoop
		case <-progressTicker.C:
			inc := atomic.LoadUint64(&included)
			fail := atomic.LoadUint64(&onChainFail)
			pe := atomic.LoadUint64(&pollErr)
			settled := inc + fail + pe
			remaining := pendingPolls - settled
			if remaining > pendingPolls {
				remaining = 0 // underflow guard
			}
			logger.Infof("poll progress -- settled=%d (included=%d failed=%d pollErr=%d) remaining=%d",
				settled, inc, fail, pe, remaining)
		}
	}

	logger.Infof("on-chain results  -- included=%d failed=%d pollErr=%d",
		included, onChainFail, pollErr)

	e2eLatencies := e2eStats.snapshot()
	if len(e2eLatencies) > 0 {
		sort.Slice(e2eLatencies, func(i, j int) bool { return e2eLatencies[i] < e2eLatencies[j] })
		var total time.Duration
		for _, d := range e2eLatencies {
			total += d
		}
		mean := total / time.Duration(len(e2eLatencies))
		p50 := percentileDuration(e2eLatencies, 0.50)
		p95 := percentileDuration(e2eLatencies, 0.95)
		p99 := percentileDuration(e2eLatencies, 0.99)
		max := e2eLatencies[len(e2eLatencies)-1]
		logger.Infof("e2e latency (submit start -> terminal poll)  count=%d mean=%s p50=%s p95=%s p99=%s max=%s timeouts=%d",
			len(e2eLatencies), mean, p50, p95, p99, max, pollErr)
	} else {
		logger.Infof("e2e latency (submit start -> terminal poll)  count=0 timeouts=%d", pollErr)
	}

	// Log Vegeta's built-in metrics: latency percentiles, achieved rate,
	// throughput (successful requests/s), and success ratio.
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

	return nil
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
