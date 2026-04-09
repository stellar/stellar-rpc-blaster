package blasterMetrics

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"

	"github.com/stellar/stellar-rpc-blaster/internal/config"

	"github.com/stellar/go-stellar-sdk/support/log"
)

var capturedPercentiles = []float64{50, 95, 99, 99.9} // treat as const

// Aggregator collects stats across all endpoints
type Aggregator struct {
	logger          *log.Entry
	writeOutputPath string

	stats            map[string]*EndpointStats
	orderedEndpoints []string

	start    time.Time
	duration time.Duration
	mu       sync.RWMutex
}

// EndpointStats collects stats for all vegeta workers of an endpoint
type EndpointStats struct {
	histogram   *hdrhistogram.Histogram
	success     uint64
	errors      uint64
	errorTypes  map[string]ErrorResult
	percentiles map[float64]time.Duration
	targetRPS   float64
	achievedRPS float64
}

// Main driver function; consumes samples from the channel and prints progress every 5 seconds
func (a *Aggregator) Run(ctx context.Context, in <-chan Sample) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case sample, ok := <-in:
			if !ok {
				// channel closed
				if err := WriteOutput(a); err != nil {
					a.logger.Error(err)
				}
				return
			}
			if err := a.Record(sample); err != nil {
				a.logger.Error(err)
			}
		case <-ticker.C:
			a.logger.Info(a)
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				a.logger.Error(fmt.Errorf("aggregator.Run terminating due to context error: %w", err))
			}
			if err := WriteOutput(a); err != nil {
				a.logger.Error(fmt.Errorf("Failed to write output results: %w", err))
			}
			return
		}
	}
}

func NewAggregator(logger *log.Entry, settings config.Config) *Aggregator {
	a := Aggregator{
		logger:          logger,
		stats:           make(map[string]*EndpointStats),
		start:           time.Now(),
		duration:        settings.Duration,
		writeOutputPath: settings.TestOutputPath,
	}
	endpoints := settings.GetEndpoints()
	slices.Sort(endpoints)
	a.orderedEndpoints = endpoints // maintain order for consistent output

	for _, endpointKey := range endpoints {
		a.stats[endpointKey] = &EndpointStats{
			histogram:   hdrhistogram.New(1, 60000000, 3),
			percentiles: make(map[float64]time.Duration),
			errorTypes:  make(map[string]ErrorResult),
		}
	}

	return &a
}

// computes and stores percentiles from the histogram
func (e *EndpointStats) refreshPercentiles() {
	for _, p := range capturedPercentiles {
		e.percentiles[p] = time.Duration(e.histogram.ValueAtPercentile(p)) * time.Microsecond
	}
}

func (a *Aggregator) Record(sample Sample) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.stats[sample.Endpoint]; !ok {
		return fmt.Errorf("unknown endpoint in sample: %s", sample.Endpoint)
	}

	epStats := a.stats[sample.Endpoint]
	epStats.targetRPS = sample.CurrentRPS
	if sample.OK {
		epStats.success++
	} else {
		epStats.errors++
		errKey := sample.Err
		if errKey == "" {
			errKey = strconv.Itoa(int(sample.Code))
		}
		if existing, ok := epStats.errorTypes[errKey]; ok {
			existing.Count++
			epStats.errorTypes[errKey] = existing
		} else {
			epStats.errorTypes[errKey] = ErrorResult{
				ErrorMsg:  sample.Err,
				ErrorCode: int(sample.Code),
				Count:     1,
			}
		}
	}
	epStats.achievedRPS = float64(epStats.success+epStats.errors) / time.Since(a.start).Seconds()
	epStats.histogram.RecordValue(int64(sample.Latency / time.Microsecond))
	return nil
}

// constructs a logging string showing progress for all endpoints
func (a *Aggregator) String() string {
	var line strings.Builder

	elapsed := time.Since(a.start).Round(time.Second)

	fmt.Fprintf(&line, "\n[%s / %s]", elapsed, a.duration)

	a.mu.RLock()
	defer a.mu.RUnlock()

	for _, endpointName := range a.orderedEndpoints {
		endpointStats := a.stats[endpointName]
		fmt.Fprintf(&line, "\n%-20s: %s", endpointName, endpointStats)
	}

	if elapsed >= a.duration {
		return "=== Final Results ===" + line.String()
	}

	return line.String()
}

// outputs a prettified one-line summary of one endpoint's stats
func (e *EndpointStats) String() string {
	e.refreshPercentiles()
	total := e.success + e.errors

	out := fmt.Sprintf("%6d req (%6d ok, %4d err) | %6.2f target RPS vs. %6.2f achieved RPS | ", total, e.success, e.errors, e.targetRPS, e.achievedRPS)
	for _, p := range capturedPercentiles {
		out += fmt.Sprintf("p%4.1f: %8s, ", p, fmtDuration(e.percentiles[p]))
	}

	return out[:len(out)-2] // trim trailing ", "
}

// Formats duration into microseconds, milliseconds or seconds
func fmtDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%4dµs", d.Microseconds())
	} else if d < time.Second {
		return fmt.Sprintf("%4.1fms", float64(d.Microseconds())/1e3)
	}
	return fmt.Sprintf("%4.1fs", float64(d.Milliseconds())/1e3)
}
