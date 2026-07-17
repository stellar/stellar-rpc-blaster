package blasterMetrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"
)

// JSON serializable final results structure, holding results for all endpoints
type Results struct {
	Start           time.Time                  `json:"start"`
	End             time.Time                  `json:"end"`
	DurationSeconds float64                    `json:"duration_seconds"`
	Endpoints       map[string]*EndpointResult `json:"-"`
}

// EndpointResult holds final stats for one endpoint
type EndpointResult struct {
	TotalRequests uint64                 `json:"total_requests"`
	Success       uint64                 `json:"success"`
	Errors        uint64                 `json:"errors"`
	TargetRPS     float64                `json:"target_rps"`
	Limit         uint64                 `json:"limit,omitempty"`
	Percentiles   map[string]float64     `json:"percentiles_ms"`
	ErrorTypes    map[string]ErrorResult `json:"error_types,omitempty"`
	Timeline      []StepSnapshot         `json:"-"`
}

// StepSnapshot captures metrics for a single step-interval window
type StepSnapshot struct {
	TargetRPS float64 `json:"target_rps"`
	Success   uint64  `json:"success"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"error_rate_pct"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	P999Ms    float64 `json:"p99.9_ms"`
}

type ErrorResult struct {
	ErrorMsg  string    `json:"error_msg"`
	ErrorCode int       `json:"error_code"`
	Count     uint64    `json:"count"`
	FirstSeen time.Time `json:"time_first_seen"`
	LastSeen  time.Time `json:"time_last_seen"`
}

// Returns the final aggregated results
func (a *Aggregator) Results() *Results {
	durationSeconds := time.Since(a.start).Round(time.Second).Seconds()
	results := &Results{
		Start:           a.start.UTC(),
		End:             time.Now().UTC(),
		DurationSeconds: durationSeconds,
		Endpoints:       make(map[string]*EndpointResult, len(a.stats)),
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, name := range a.orderedEndpoints {
		stats := a.stats[name]
		stats.refreshPercentiles() // compute final percentiles from histogram
		totalRequests := stats.success + stats.errors
		errorTypesCopy := make(map[string]ErrorResult, len(stats.errorTypes))
		maps.Copy(errorTypesCopy, stats.errorTypes)
		var timeline []StepSnapshot
		for _, w := range stats.windows {
			total := w.success + w.errors
			var errorRatePct float64
			if total > 0 {
				errorRatePct = math.Round(float64(w.errors)/float64(total)*10000) / 100 // e.g. 4.52%
			}
			timeline = append(timeline, StepSnapshot{
				TargetRPS: math.Round(w.targetRPS*100) / 100,
				Success:   w.success,
				Errors:    w.errors,
				ErrorRate: errorRatePct,
				P50Ms:     float64(w.histogram.ValueAtPercentile(50)) / 1e3,
				P95Ms:     float64(w.histogram.ValueAtPercentile(95)) / 1e3,
				P99Ms:     float64(w.histogram.ValueAtPercentile(99)) / 1e3,
				P999Ms:    float64(w.histogram.ValueAtPercentile(99.9)) / 1e3,
			})
		}

		results.Endpoints[name] = &EndpointResult{
			TotalRequests: totalRequests,
			Success:       stats.success,
			Errors:        stats.errors,
			ErrorTypes:    errorTypesCopy,
			TargetRPS:     stats.targetRPS,
			Limit:         stats.limit,
			Percentiles:   make(map[string]float64),
			Timeline:      timeline,
		}
		for p, d := range stats.percentiles {
			key := fmt.Sprintf("p%.1f", p)
			results.Endpoints[name].Percentiles[key] = float64(d.Nanoseconds()) / 1e6 // ms
		}
	}

	return results
}

// marshalOpen marshals v as indented JSON and returns everything up to (not including) the
// closing '}', so the caller can append more fields before closing the object.
func marshalOpen(v any, prefix, indent string) ([]byte, error) {
	data, err := json.MarshalIndent(v, prefix, indent)
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(data[:bytes.LastIndexByte(data, '}')], "\n "+prefix), nil
}

// MarshalJSON produces indented JSON but keeps each timeline entry on a single line.
func (r Results) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	type resultsAlias Results
	top, err := marshalOpen(resultsAlias(r), "", "  ")
	if err != nil {
		return nil, err
	}
	buf.Write(top)
	buf.WriteString(",\n  \"endpoints\": {")

	// Write each endpoint's main stats as indented JSON
	for i, name := range slices.Sorted(maps.Keys(r.Endpoints)) {
		ep := r.Endpoints[name]
		if i > 0 {
			buf.WriteByte(',')
		}
		epJson, err := marshalOpen(ep, "    ", "  ")
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&buf, "\n    %q: ", name)
		buf.Write(epJson)

		// Write timeline as compact JSON array
		if len(ep.Timeline) > 0 {
			buf.WriteString(",\n      \"timeline\": [\n")
			for _, snap := range ep.Timeline {
				snapJson, err := json.Marshal(snap)
				if err != nil {
					return nil, err
				}
				fmt.Fprintf(&buf, "        %s,\n", snapJson)
			}
			buf.Truncate(buf.Len() - 2)
			buf.WriteString("\n      ]")
		}
		buf.WriteString("\n    }")
	}

	buf.WriteString("\n  }\n}")
	return buf.Bytes(), nil
}
