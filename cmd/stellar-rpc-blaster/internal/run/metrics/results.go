package blasterMetrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/run/parameters"
)

// JSON serializable final results structure, holding results for all endpoints
type Results struct {
	Start           time.Time                  `json:"start"`
	End             time.Time                  `json:"end"`
	Seed            uint64                     `json:"seed"`
	DurationSeconds float64                    `json:"duration_seconds"`
	Endpoints       map[string]*EndpointResult `json:"-"`
}

// EndpointResult holds final stats for one endpoint
type EndpointResult struct {
	TotalRequests uint64                     `json:"total_requests"`
	Success       uint64                     `json:"success"`
	Errors        uint64                     `json:"errors"`
	TargetRPS     float64                    `json:"target_rps"`
	Limit         uint64                     `json:"limit,omitempty"`
	Profile       int                        `json:"traffic_profile,omitempty"` // version of the hard-coded traffic model, for cross-run comparability
	Percentiles   map[string]float64         `json:"percentiles_ms"`
	ErrorTypes    map[string]ErrorResult     `json:"error_types,omitempty"`
	Timeline      []StepSnapshot             `json:"-"`
	Archetypes    map[string]*EndpointResult `json:"-"` // per-archetype sub-stream results, written after the overall entry
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
		Seed:            a.rngSeed,
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

		endpoint, archetype, isSubStream := strings.Cut(name, "/")
		entry := &EndpointResult{
			TotalRequests: totalRequests,
			Success:       stats.success,
			Errors:        stats.errors,
			ErrorTypes:    errorTypesCopy,
			TargetRPS:     stats.targetRPS,
			Limit:         stats.limit,
			Profile:       parameters.ProfileVersion(endpoint),
			Percentiles:   make(map[string]float64),
			Timeline:      timeline,
		}
		for p, d := range stats.percentiles {
			entry.Percentiles[fmt.Sprintf("p%.1f", p)] = float64(d.Nanoseconds()) / 1e6 // ms
		}
		if !isSubStream {
			results.Endpoints[name] = entry
			continue
		}
		// orderedEndpoints is sorted, so an endpoint precedes its sub-streams
		parent := results.Endpoints[endpoint]
		if parent.Archetypes == nil {
			parent.Archetypes = make(map[string]*EndpointResult)
		}
		parent.Archetypes[archetype] = entry
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

// writeEndpoint writes one endpoint entry as indented JSON at the given pad,
// keeping each timeline entry on a single line.
func writeEndpoint(buf *bytes.Buffer, pad, name string, ep *EndpointResult) error {
	epJson, err := marshalOpen(ep, pad, "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(buf, "\n%s%q: ", pad, name)
	buf.Write(epJson)

	if len(ep.Timeline) > 0 {
		fmt.Fprintf(buf, ",\n%s  \"timeline\": [\n", pad)
		for _, snap := range ep.Timeline {
			snapJson, err := json.Marshal(snap)
			if err != nil {
				return err
			}
			fmt.Fprintf(buf, "%s    %s,\n", pad, snapJson)
		}
		buf.Truncate(buf.Len() - 2)
		fmt.Fprintf(buf, "\n%s  ]", pad)
	}
	if len(ep.Archetypes) > 0 {
		fmt.Fprintf(buf, ",\n%s  \"archetypes\": {", pad)
		for i, name := range slices.Sorted(maps.Keys(ep.Archetypes)) {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeEndpoint(buf, pad+"    ", name, ep.Archetypes[name]); err != nil {
				return err
			}
		}
		fmt.Fprintf(buf, "\n%s  }", pad)
	}
	fmt.Fprintf(buf, "\n%s}", pad)
	return nil
}

// MarshalJSON produces indented JSON, keeping each timeline entry on a single line
// and nesting archetype sub-stream results inside their endpoint's entry.
func (r Results) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	type resultsAlias Results
	top, err := marshalOpen(resultsAlias(r), "", "  ")
	if err != nil {
		return nil, err
	}
	buf.Write(top)
	buf.WriteString(",\n  \"endpoints\": {")

	for i, name := range slices.Sorted(maps.Keys(r.Endpoints)) {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeEndpoint(&buf, "    ", name, r.Endpoints[name]); err != nil {
			return nil, err
		}
	}

	buf.WriteString("\n  }\n}")
	return buf.Bytes(), nil
}
