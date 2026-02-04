package blasterMetrics

import (
	"fmt"
	"time"
)

// JSON serializable final results structure, holding results for all endpoints
type Results struct {
	DurationSeconds float64                    `json:"duration_seconds"`
	Endpoints       map[string]*EndpointResult `json:"endpoints"`
}

// EndpointResult holds final stats for one endpoint
type EndpointResult struct {
	TotalRequests uint64                 `json:"total_requests"`
	Success       uint64                 `json:"success"`
	Errors        uint64                 `json:"errors"`
	FinalRPS      int                    `json:"target_rps"`
	AchievedRPS   float64                `json:"achieved_rps"`
	Percentiles   map[string]float64     `json:"percentiles_ms"`
	ErrorTypes    map[string]ErrorResult `json:"error_types,omitempty"`
}

type ErrorResult struct {
	ErrorMsg  string `json:"error_msg"`
	ErrorCode int    `json:"error_code"`
	Count     uint64 `json:"count"`
}

// Returns the final aggregated results
func (a *Aggregator) Results() *Results {
	a.mu.RLock()
	defer a.mu.RUnlock()

	durationSeconds := time.Since(a.start).Round(time.Second).Seconds()
	results := &Results{
		DurationSeconds: durationSeconds,
		Endpoints:       make(map[string]*EndpointResult, len(a.stats)),
	}

	for _, name := range a.orderedEndpoints {
		stats := a.stats[name]
		stats.refreshPercentiles() // compute final percentiles from histogram
		totalRequests := stats.success + stats.errors
		results.Endpoints[name] = &EndpointResult{
			TotalRequests: totalRequests,
			Success:       stats.success,
			Errors:        stats.errors,
			ErrorTypes:    stats.errorTypes,
			FinalRPS:      stats.targetRPS,
			AchievedRPS:   stats.achievedRPS,
			Percentiles:   make(map[string]float64),
		}
		for p, d := range stats.percentiles {
			key := fmt.Sprintf("p%.1f", p)
			results.Endpoints[name].Percentiles[key] = float64(d.Nanoseconds()) / 1e6 // ms
		}
	}

	return results
}
