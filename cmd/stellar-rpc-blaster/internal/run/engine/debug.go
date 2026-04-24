package engine

// DEBUG SCAFFOLDING — temporary file for verifying probability distributions
// Remove after confirming request generation is correct.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync/atomic"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

// DebugRequestCounter tracks how many times a targeter is called (thread-safe)
type DebugRequestCounter struct {
	count atomic.Uint64
}

func (c *DebugRequestCounter) Inc() uint64   { return c.count.Add(1) }
func (c *DebugRequestCounter) Total() uint64 { return c.count.Load() }

// jsonRPCRequest is used to decode pre-built request bodies for analysis
type jsonRPCRequest struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

type debugDistribution struct {
	label    string
	actual   float64
	expected float64
	count    int
	total    int
}

// DebugAnalyzeBodies decodes pre-built JSON-RPC request bodies and reports
// parameter distributions vs expected probabilities for each endpoint.
func DebugAnalyzeBodies(logger *log.Entry, endpointKey string, bodies [][]byte) {
	if len(bodies) == 0 {
		logger.Infof("[DEBUG] %s: no bodies to analyze", endpointKey)
		return
	}

	requests := make([]jsonRPCRequest, 0, len(bodies))
	for _, b := range bodies {
		var req jsonRPCRequest
		if err := json.Unmarshal(b, &req); err != nil {
			logger.Warnf("[DEBUG] Failed to decode body for %s: %v", endpointKey, err)
			continue
		}
		requests = append(requests, req)
	}

	var dists []debugDistribution

	switch endpointKey {
	case "getTransaction":
		dists = analyzeGetTransaction(requests)
	case "getLedgerEntries":
		dists = analyzeGetLedgerEntries(requests)
	case "getTransactions", "getLedgers":
		dists = analyzePagedEndpoint(requests)
	case "getEvents":
		dists = analyzeGetEvents(requests)
	default:
		logger.Infof("[DEBUG] %s: static endpoint, no parameter distributions to check", endpointKey)
		return
	}

	if len(dists) > 0 {
		printDebugReport(logger, endpointKey, len(requests), dists)
	}
}

// --- Per-endpoint analyzers ---

func analyzeGetTransaction(requests []jsonRPCRequest) []debugDistribution {
	n := len(requests)
	jsonCount, notFoundCount := 0, 0
	dummyHash := "0000000000000000000000000000000000000000000000000000000000000000"

	for _, req := range requests {
		if f, ok := req.Params["format"].(string); ok && f == "json" {
			jsonCount++
		}
		if hash, ok := req.Params["hash"].(string); ok && hash == dummyHash {
			notFoundCount++
		}
	}

	return []debugDistribution{
		{"format=json", pct(jsonCount, n), util.PrJson, jsonCount, n},
	}
}

func analyzeGetLedgerEntries(requests []jsonRPCRequest) []debugDistribution {
	n := len(requests)
	jsonCount, singleKey, midKey, highKey := 0, 0, 0, 0

	for _, req := range requests {
		if f, ok := req.Params["format"].(string); ok && f == "json" {
			jsonCount++
		}
		if keys, ok := req.Params["keys"].([]any); ok {
			klen := len(keys)
			switch {
			case klen == 1:
				singleKey++
			case klen >= 2 && klen <= 10:
				midKey++
			case klen >= 50:
				highKey++
				// keys in 11-49 range fall through (gap in spec)
			}
		}
	}

	return []debugDistribution{
		{"format=json", pct(jsonCount, n), util.PrJson, jsonCount, n},
		{"keys=1", pct(singleKey, n), util.PrKeyCount[0], singleKey, n},
		{"keys=[2,10]", pct(midKey, n), util.PrKeyCount[1], midKey, n},
		{"keys=[50,200]", pct(highKey, n), util.PrKeyCount[2], highKey, n},
	}
}

func analyzePagedEndpoint(requests []jsonRPCRequest) []debugDistribution {
	n := len(requests)
	jsonCount, cursorCount := 0, 0

	for _, req := range requests {
		if f, ok := req.Params["format"].(string); ok && f == "json" {
			jsonCount++
		}
		if pag, ok := req.Params["pagination"].(map[string]any); ok {
			if _, hasCursor := pag["cursor"]; hasCursor {
				cursorCount++
			}
		}
	}

	return []debugDistribution{
		{"format=json", pct(jsonCount, n), util.PrJson, jsonCount, n},
		{"has_cursor", pct(cursorCount, n), util.PrCursor, cursorCount, n},
	}
}

func analyzeGetEvents(requests []jsonRPCRequest) []debugDistribution {
	contractCount, multiContract, typeCount, topicCount := 0, 0, 0, 0
	contractFiltered := 0 // denominator for multi-contract ratio

	for _, req := range requests {
		filters, ok := req.Params["filters"].([]any)
		if !ok || len(filters) == 0 {
			continue
		}
		filter, ok := filters[0].(map[string]any)
		if !ok {
			continue
		}

		if ids, ok := filter["contractIds"].([]any); ok {
			contractCount++
			contractFiltered++
			if len(ids) > 1 {
				multiContract++
			}
		}
		if _, ok := filter["type"]; ok {
			typeCount++
		}
		if _, ok := filter["topics"]; ok {
			topicCount++
		}
	}

	return []debugDistribution{}
}

// --- Reporting helpers ---

func pct(count, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total)
}

func tolerance(n int) float64 {
	// ~2 standard deviations for a binomial proportion with p≈0.5
	return math.Max(0.10, 2.0/math.Sqrt(float64(n)))
}

func printDebugReport(logger *log.Entry, endpoint string, total int, dists []debugDistribution) {
	tol := tolerance(total)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n╔══ DEBUG: %s (%d bodies, tolerance ±%.0f%%) ══\n", endpoint, total, tol*100))
	sb.WriteString(fmt.Sprintf("║ %-32s %8s %8s %8s %6s\n", "Dimension", "Actual", "Expected", "Delta", "Check"))
	sb.WriteString("║ " + strings.Repeat("─", 68) + "\n")

	allPass := true
	for _, d := range dists {
		delta := d.actual - d.expected
		status := "  OK"
		if math.Abs(delta) > tol {
			status = "DRIFT"
			allPass = false
		}
		sb.WriteString(fmt.Sprintf("║ %-32s %7.1f%% %7.1f%% %+7.1f%% %6s  (%d/%d)\n",
			d.label,
			d.actual*100, d.expected*100, delta*100,
			status,
			d.count, d.total,
		))
	}

	if allPass {
		sb.WriteString("╚══ RESULT: ALL PASS ══\n")
	} else {
		sb.WriteString("╚══ RESULT: DRIFT DETECTED — review flagged dimensions ══\n")
	}

	logger.Info(sb.String())
}

// DebugPrintFireCounts logs how many requests each endpoint actually fired
func DebugPrintFireCounts(logger *log.Entry, counters map[string]*DebugRequestCounter, durationSecs float64) {
	var sb strings.Builder
	sb.WriteString("\n╔══ DEBUG: ACTUAL REQUEST FIRE COUNTS ══\n")
	sb.WriteString(fmt.Sprintf("║ %-25s %10s %10s\n", "Endpoint", "Total", "Avg RPS"))
	sb.WriteString("║ " + strings.Repeat("─", 50) + "\n")

	var grandTotal uint64
	for endpoint, counter := range counters {
		total := counter.Total()
		grandTotal += total
		avgRPS := float64(total) / durationSecs
		sb.WriteString(fmt.Sprintf("║ %-25s %10d %10.1f\n", endpoint, total, avgRPS))
	}
	sb.WriteString("║ " + strings.Repeat("─", 50) + "\n")
	sb.WriteString(fmt.Sprintf("║ %-25s %10d %10.1f\n", "TOTAL", grandTotal, float64(grandTotal)/durationSecs))
	sb.WriteString("╚══════════════════════════════════════\n")

	logger.Info(sb.String())
}
