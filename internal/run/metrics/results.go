package blasterMetrics

import (
	"fmt"
	"maps"
	"time"
)

// JSON serializable final results structure, holding results for all endpoints
type Results struct {
	Start           time.Time                  `json:"start"`
	End             time.Time                  `json:"end"`
	DurationSeconds float64                    `json:"duration_seconds"`
	Endpoints       map[string]*EndpointResult `json:"endpoints"`
}

// EndpointResult holds final stats for one endpoint
type EndpointResult struct {
	TotalRequests uint64                 `json:"total_requests"`
	Success       uint64                 `json:"success"`
	Errors        uint64                 `json:"errors"`
	TargetRPS     float64                `json:"target_rps"`
	Percentiles   map[string]float64     `json:"percentiles_ms"`
	ErrorTypes    map[string]ErrorResult `json:"error_types,omitempty"`
}

type ErrorResult struct {
	ErrorMsg  string `json:"error_msg"`
	ErrorCode int    `json:"error_code"`
	Count     uint64 `json:"count"`
	TimeSeen  struct {
		FirstSeen time.Time `json:"time_first_seen"`
		LastSeen  time.Time `json:"time_last_seen"`
	} `json:"times_seen"`
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
		results.Endpoints[name] = &EndpointResult{
			TotalRequests: totalRequests,
			Success:       stats.success,
			Errors:        stats.errors,
			ErrorTypes:    errorTypesCopy,
			TargetRPS:     stats.targetRPS,
			Percentiles:   make(map[string]float64),
		}
		for p, d := range stats.percentiles {
			key := fmt.Sprintf("p%.1f", p)
			results.Endpoints[name].Percentiles[key] = float64(d.Nanoseconds()) / 1e6 // ms
		}
	}

	return results
}
