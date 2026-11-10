package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
)

type benchmarkMetricsReport struct {
	GeneratedAt       time.Time            `json:"generated_at"`
	Mode              config.BenchmarkMode `json:"mode"`
	RPCURL            string               `json:"rpc_url"`
	NetworkPassphrase string               `json:"network_passphrase"`
	NumberOfAccounts  int                  `json:"number_of_accounts"`
	Duration          durationMetric       `json:"duration"`
	RampUp            durationMetric       `json:"ramp_up"`
	TargetRPS         int                  `json:"target_rps"`
	ClassicRPS        int                  `json:"classic_rps"`
	Workloads         []workloadMetricsReport
}

type workloadMetricsReport struct {
	Workload         string               `json:"workload"`
	RateSummary      string               `json:"rate_summary"`
	Duration         durationMetric       `json:"duration"`
	RampUp           durationMetric       `json:"ramp_up"`
	TargetRPS        int                  `json:"target_rps"`
	PollWorkers      int                  `json:"poll_workers"`
	PollDrainTimeout durationMetric       `json:"poll_drain_timeout"`
	Submission       submissionMetrics    `json:"submission"`
	OnChain          onChainMetrics       `json:"on_chain"`
	E2ELatency       latencyMetricsReport `json:"e2e_latency"`
	Ledger           ledgerMetricsReport  `json:"ledger"`
	Vegeta           vegetaMetricsReport  `json:"vegeta"`
}

type durationMetric struct {
	String       string  `json:"string"`
	Seconds      float64 `json:"seconds"`
	Milliseconds float64 `json:"milliseconds"`
}

type submissionMetrics struct {
	Submitted     uint64 `json:"submitted"`
	HTTPError     uint64 `json:"http_error"`
	Queued        uint64 `json:"queued"`
	TryAgainLater uint64 `json:"try_again_later"`
	SubmitErrors  uint64 `json:"submit_errors"`
	Ambiguous     uint64 `json:"ambiguous"`
}

type onChainMetrics struct {
	Included uint64 `json:"included"`
	Failed   uint64 `json:"failed"`
	PollErr  uint64 `json:"poll_error"`
}

type countMetric struct {
	Code  string `json:"code"`
	Count int64  `json:"count"`
}

type latencyMetricsReport struct {
	Count    int            `json:"count"`
	Mean     durationMetric `json:"mean"`
	P50      durationMetric `json:"p50"`
	P95      durationMetric `json:"p95"`
	P99      durationMetric `json:"p99"`
	Max      durationMetric `json:"max"`
	Timeouts uint64         `json:"timeouts,omitempty"`
}

type ledgerMetricsReport struct {
	FinalityDistance      ledgerDistanceMetrics `json:"finality_distance"`
	TimeoutDistance       ledgerDistanceMetrics `json:"timeout_distance"`
	TransactionsPerLedger txsPerLedgerMetrics   `json:"transactions_per_finality_ledger"`
}

type ledgerDistanceMetrics struct {
	Count   int     `json:"count"`
	Mean    float64 `json:"mean"`
	P50     uint32  `json:"p50"`
	P95     uint32  `json:"p95"`
	P99     uint32  `json:"p99"`
	Max     uint32  `json:"max"`
	Skipped uint64  `json:"skipped"`
}

type txsPerLedgerMetrics struct {
	LedgerCount int     `json:"ledger_count"`
	FirstLedger uint32  `json:"first_ledger"`
	LastLedger  uint32  `json:"last_ledger"`
	Total       uint64  `json:"total"`
	Mean        float64 `json:"mean"`
	P50         uint32  `json:"p50"`
	P95         uint32  `json:"p95"`
	P99         uint32  `json:"p99"`
	Max         uint32  `json:"max"`
}

type vegetaMetricsReport struct {
	Requests       uint64               `json:"requests"`
	Rate           float64              `json:"rate"`
	Throughput     float64              `json:"throughput"`
	SuccessRatio   float64              `json:"success_ratio"`
	SuccessPercent float64              `json:"success_percent"`
	Latency        latencyMetricsReport `json:"latency"`
	BytesIn        byteMetricsReport    `json:"bytes_in"`
	BytesOut       byteMetricsReport    `json:"bytes_out"`
	Duration       durationMetric       `json:"duration"`
	Wait           durationMetric       `json:"wait"`
	StatusCodes    map[string]int       `json:"status_codes"`
	Errors         []countMetric        `json:"errors"`
}

type byteMetricsReport struct {
	Total uint64  `json:"total"`
	Mean  float64 `json:"mean"`
}

type flatMetricsRecord map[string]any

func DefaultMetricsFileName(mode config.BenchmarkMode, now time.Time) string {
	return filepath.Join("metrics", fmt.Sprintf("tx-load-test-metrics-%s-%s.ndjson", now.UTC().Format("20060102T150405Z"), sanitizeFilenamePart(string(mode))))
}

func sanitizeFilenamePart(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func newBenchmarkMetricsReport(cfg config.Config, workloads []workloadMetricsReport) benchmarkMetricsReport {
	return benchmarkMetricsReport{
		GeneratedAt:       time.Now().UTC(),
		Mode:              cfg.Mode,
		RPCURL:            cfg.RPCURL,
		NetworkPassphrase: cfg.NetworkPassphrase,
		NumberOfAccounts:  cfg.NumberOfAccounts,
		Duration:          newDurationMetric(cfg.Duration),
		RampUp:            newDurationMetric(cfg.RampUp),
		TargetRPS:         cfg.TargetRPS,
		ClassicRPS:        cfg.ClassicRPS,
		Workloads:         workloads,
	}
}

func newWorkloadMetricsReport(label string, rateSummary string, cfg config.Config, numPollWorkers int, pollDrainTimeout time.Duration, state *attackState, metrics vegeta.Metrics) workloadMetricsReport {
	submitted, httpErr, queued, tryAgainLater, submitErrors, ambiguous := state.submissionSnapshot()
	included, onChainFail, pollErr := state.pollSnapshot()
	return workloadMetricsReport{
		Workload:         label,
		RateSummary:      rateSummary,
		Duration:         newDurationMetric(cfg.Duration),
		RampUp:           newDurationMetric(cfg.RampUp),
		TargetRPS:        cfg.TargetRPS,
		PollWorkers:      numPollWorkers,
		PollDrainTimeout: newDurationMetric(pollDrainTimeout),
		Submission:       submissionMetrics{submitted, httpErr, queued, tryAgainLater, submitErrors, ambiguous},
		OnChain:          onChainMetrics{Included: included, Failed: onChainFail, PollErr: pollErr},
		E2ELatency:       newE2ELatencyMetricsReport(state.e2eStats.snapshot(), pollErr),
		Ledger:           newLedgerMetricsReport(state.ledgerStats.snapshot()),
		Vegeta:           newVegetaMetricsReport(metrics, state.vegetaErrors.entries()),
	}
}

func newDurationMetric(d time.Duration) durationMetric {
	return durationMetric{String: d.String(), Seconds: d.Seconds(), Milliseconds: float64(d) / float64(time.Millisecond)}
}

func newCountMetrics(entries []rejectionCountEntry) []countMetric {
	if len(entries) == 0 {
		return nil
	}
	out := make([]countMetric, len(entries))
	for i, entry := range entries {
		out[i] = countMetric{Code: entry.code, Count: entry.count}
	}
	return out
}

func newE2ELatencyMetricsReport(latencies []time.Duration, timeouts uint64) latencyMetricsReport {
	report := newLatencyMetricsReport(latencies)
	report.Timeouts = timeouts
	return report
}

func newLatencyMetricsReport(latencies []time.Duration) latencyMetricsReport {
	if len(latencies) == 0 {
		return latencyMetricsReport{}
	}
	slices.Sort(latencies)
	var total time.Duration
	for _, d := range latencies {
		total += d
	}
	mean := total / time.Duration(len(latencies))
	return latencyMetricsReport{
		Count: len(latencies),
		Mean:  newDurationMetric(mean),
		P50:   newDurationMetric(percentileDuration(latencies, 0.50)),
		P95:   newDurationMetric(percentileDuration(latencies, 0.95)),
		P99:   newDurationMetric(percentileDuration(latencies, 0.99)),
		Max:   newDurationMetric(latencies[len(latencies)-1]),
	}
}

func newLedgerMetricsReport(snapshot ledgerMetricSnapshot) ledgerMetricsReport {
	return ledgerMetricsReport{
		FinalityDistance:      newLedgerDistanceMetrics(snapshot.finalityDistances, snapshot.finalitySkipped),
		TimeoutDistance:       newLedgerDistanceMetrics(snapshot.timeoutDistances, snapshot.timeoutSkipped),
		TransactionsPerLedger: newTxsPerLedgerMetrics(snapshot.txsPerLedger),
	}
}

func newLedgerDistanceMetrics(distances []uint32, skipped uint64) ledgerDistanceMetrics {
	if len(distances) == 0 {
		return ledgerDistanceMetrics{Skipped: skipped}
	}
	slices.Sort(distances)
	var total uint64
	for _, distance := range distances {
		total += uint64(distance)
	}
	return ledgerDistanceMetrics{
		Count:   len(distances),
		Mean:    float64(total) / float64(len(distances)),
		P50:     percentileUint32(distances, 0.50),
		P95:     percentileUint32(distances, 0.95),
		P99:     percentileUint32(distances, 0.99),
		Max:     distances[len(distances)-1],
		Skipped: skipped,
	}
}

func newTxsPerLedgerMetrics(txsPerLedger map[uint32]uint32) txsPerLedgerMetrics {
	if len(txsPerLedger) == 0 {
		return txsPerLedgerMetrics{}
	}
	ledgers := make([]uint32, 0, len(txsPerLedger))
	counts := make([]uint32, 0, len(txsPerLedger))
	var total uint64
	for ledger, count := range txsPerLedger {
		ledgers = append(ledgers, ledger)
		counts = append(counts, count)
		total += uint64(count)
	}
	slices.Sort(ledgers)
	slices.Sort(counts)
	return txsPerLedgerMetrics{
		LedgerCount: len(counts),
		FirstLedger: ledgers[0],
		LastLedger:  ledgers[len(ledgers)-1],
		Total:       total,
		Mean:        float64(total) / float64(len(counts)),
		P50:         percentileUint32(counts, 0.50),
		P95:         percentileUint32(counts, 0.95),
		P99:         percentileUint32(counts, 0.99),
		Max:         counts[len(counts)-1],
	}
}

func newVegetaMetricsReport(metrics vegeta.Metrics, errorCounts []rejectionCountEntry) vegetaMetricsReport {
	statusCodes := make(map[string]int, len(metrics.StatusCodes))
	for code, count := range metrics.StatusCodes {
		statusCodes[code] = count
	}
	return vegetaMetricsReport{
		Requests:       metrics.Requests,
		Rate:           metrics.Rate,
		Throughput:     metrics.Throughput,
		SuccessRatio:   metrics.Success,
		SuccessPercent: metrics.Success * 100,
		Latency: latencyMetricsReport{
			Count: int(metrics.Requests),
			Mean:  newDurationMetric(metrics.Latencies.Mean),
			P50:   newDurationMetric(metrics.Latencies.P50),
			P95:   newDurationMetric(metrics.Latencies.P95),
			P99:   newDurationMetric(metrics.Latencies.P99),
			Max:   newDurationMetric(metrics.Latencies.Max),
		},
		BytesIn:     byteMetricsReport{Total: metrics.BytesIn.Total, Mean: metrics.BytesIn.Mean},
		BytesOut:    byteMetricsReport{Total: metrics.BytesOut.Total, Mean: metrics.BytesOut.Mean},
		Duration:    newDurationMetric(metrics.Duration),
		Wait:        newDurationMetric(metrics.Wait),
		StatusCodes: statusCodes,
		Errors:      newVegetaErrorMetrics(metrics.Errors, errorCounts),
	}
}

func newVegetaErrorMetrics(errors []string, errorCounts []rejectionCountEntry) []countMetric {
	if len(errorCounts) > 0 {
		return newCountMetrics(errorCounts)
	}
	if len(errors) == 0 {
		return nil
	}
	counts := make(map[string]int64, len(errors))
	for _, errMsg := range errors {
		counts[errMsg]++
	}
	entries := make([]rejectionCountEntry, 0, len(counts))
	for errMsg, count := range counts {
		entries = append(entries, rejectionCountEntry{code: errMsg, count: count})
	}
	slices.SortFunc(entries, func(a, b rejectionCountEntry) int {
		if b.count != a.count {
			if b.count > a.count {
				return 1
			}
			return -1
		}
		return strings.Compare(a.code, b.code)
	})
	return newCountMetrics(entries)
}

func newFlatMetricsRecords(report benchmarkMetricsReport) []flatMetricsRecord {
	records := make([]flatMetricsRecord, 0, len(report.Workloads))
	for _, workload := range report.Workloads {
		base := baseFlatMetricsRecord(report, workload)

		summary := cloneFlatMetricsRecord(base)
		summary["record_type"] = "summary"
		addDurationFields(summary, "workload_duration", workload.Duration)
		addDurationFields(summary, "workload_ramp_up", workload.RampUp)
		summary["workload_target_rps"] = workload.TargetRPS
		summary["poll_workers"] = workload.PollWorkers
		addDurationFields(summary, "poll_drain_timeout", workload.PollDrainTimeout)
		addSubmissionFields(summary, "submission", workload.Submission)
		addOnChainFields(summary, "on_chain", workload.OnChain)
		addLatencyFields(summary, "e2e_latency", workload.E2ELatency, true)
		addLedgerDistanceFields(summary, "ledger_finality_distance", workload.Ledger.FinalityDistance)
		addLedgerDistanceFields(summary, "ledger_timeout_distance", workload.Ledger.TimeoutDistance)
		addTxsPerLedgerFields(summary, "ledger_transactions_per_finality_ledger", workload.Ledger.TransactionsPerLedger)
		addVegetaFields(summary, workload.Vegeta)
		addStatusCodeFields(summary, workload.Vegeta.StatusCodes)
		records = append(records, summary)

		appendCountRecords(&records, base, "vegeta_error", workload.Vegeta.Errors)
	}
	return records
}

func baseFlatMetricsRecord(report benchmarkMetricsReport, workload workloadMetricsReport) flatMetricsRecord {
	record := flatMetricsRecord{
		"generated_at":           report.GeneratedAt,
		"run_mode":               report.Mode,
		"run_rpc_url":            report.RPCURL,
		"run_network_passphrase": report.NetworkPassphrase,
		"run_number_of_accounts": report.NumberOfAccounts,
		"run_target_rps":         report.TargetRPS,
		"run_classic_rps":        report.ClassicRPS,
		"workload":               workload.Workload,
		"workload_rate_summary":  workload.RateSummary,
	}
	addDurationFields(record, "run_duration", report.Duration)
	addDurationFields(record, "run_ramp_up", report.RampUp)
	return record
}

func cloneFlatMetricsRecord(record flatMetricsRecord) flatMetricsRecord {
	clone := make(flatMetricsRecord, len(record)+32)
	for key, value := range record {
		clone[key] = value
	}
	return clone
}

func addDurationFields(record flatMetricsRecord, prefix string, metric durationMetric) {
	record[prefix+"_string"] = metric.String
	record[prefix+"_seconds"] = metric.Seconds
	record[prefix+"_milliseconds"] = metric.Milliseconds
}

func addSubmissionFields(record flatMetricsRecord, prefix string, metrics submissionMetrics) {
	record[prefix+"_submitted"] = metrics.Submitted
	record[prefix+"_http_error"] = metrics.HTTPError
	record[prefix+"_queued"] = metrics.Queued
	record[prefix+"_try_again_later"] = metrics.TryAgainLater
	record[prefix+"_submit_errors"] = metrics.SubmitErrors
	record[prefix+"_ambiguous"] = metrics.Ambiguous
}

func addOnChainFields(record flatMetricsRecord, prefix string, metrics onChainMetrics) {
	record[prefix+"_included"] = metrics.Included
	record[prefix+"_failed"] = metrics.Failed
	record[prefix+"_poll_error"] = metrics.PollErr
}

func addLatencyFields(record flatMetricsRecord, prefix string, metrics latencyMetricsReport, includeTimeouts bool) {
	record[prefix+"_count"] = metrics.Count
	addDurationFields(record, prefix+"_mean", metrics.Mean)
	addDurationFields(record, prefix+"_p50", metrics.P50)
	addDurationFields(record, prefix+"_p95", metrics.P95)
	addDurationFields(record, prefix+"_p99", metrics.P99)
	addDurationFields(record, prefix+"_max", metrics.Max)
	if includeTimeouts {
		record[prefix+"_timeouts"] = metrics.Timeouts
	}
}

func addLedgerDistanceFields(record flatMetricsRecord, prefix string, metrics ledgerDistanceMetrics) {
	record[prefix+"_count"] = metrics.Count
	record[prefix+"_mean"] = metrics.Mean
	record[prefix+"_p50"] = metrics.P50
	record[prefix+"_p95"] = metrics.P95
	record[prefix+"_p99"] = metrics.P99
	record[prefix+"_max"] = metrics.Max
	record[prefix+"_skipped"] = metrics.Skipped
}

func addTxsPerLedgerFields(record flatMetricsRecord, prefix string, metrics txsPerLedgerMetrics) {
	record[prefix+"_ledger_count"] = metrics.LedgerCount
	record[prefix+"_first_ledger"] = metrics.FirstLedger
	record[prefix+"_last_ledger"] = metrics.LastLedger
	record[prefix+"_total"] = metrics.Total
	record[prefix+"_mean"] = metrics.Mean
	record[prefix+"_p50"] = metrics.P50
	record[prefix+"_p95"] = metrics.P95
	record[prefix+"_p99"] = metrics.P99
	record[prefix+"_max"] = metrics.Max
}

func addVegetaFields(record flatMetricsRecord, metrics vegetaMetricsReport) {
	record["vegeta_requests"] = metrics.Requests
	record["vegeta_rate"] = metrics.Rate
	record["vegeta_throughput"] = metrics.Throughput
	record["vegeta_success_ratio"] = metrics.SuccessRatio
	record["vegeta_success_percent"] = metrics.SuccessPercent
	addLatencyFields(record, "vegeta_latency", metrics.Latency, false)
	record["vegeta_bytes_in_total"] = metrics.BytesIn.Total
	record["vegeta_bytes_in_mean"] = metrics.BytesIn.Mean
	record["vegeta_bytes_out_total"] = metrics.BytesOut.Total
	record["vegeta_bytes_out_mean"] = metrics.BytesOut.Mean
	addDurationFields(record, "vegeta_duration", metrics.Duration)
	addDurationFields(record, "vegeta_wait", metrics.Wait)
}

func addStatusCodeFields(record flatMetricsRecord, statusCodes map[string]int) {
	for code, count := range statusCodes {
		record["vegeta_status_code_"+sanitizeMetricFieldPart(code)] = count
	}
}

func sanitizeMetricFieldPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func appendCountRecords(records *[]flatMetricsRecord, base flatMetricsRecord, recordType string, counts []countMetric) {
	for _, count := range counts {
		record := cloneFlatMetricsRecord(base)
		record["record_type"] = recordType
		record["code"] = count.Code
		record["count"] = count.Count
		*records = append(*records, record)
	}
}

func writeBenchmarkMetricsReport(path string, report benchmarkMetricsReport) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create metrics directory %q: %w", dir, err)
		}
	}
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open metrics file %q: %w", tmpPath, err)
	}
	enc := json.NewEncoder(file)
	enc.SetEscapeHTML(false)
	for _, record := range newFlatMetricsRecords(report) {
		if err := enc.Encode(record); err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("write metrics file %q: %w", tmpPath, err)
		}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close metrics file %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename metrics file %q to %q: %w", tmpPath, path, err)
	}
	return nil
}
