package blasterMetrics

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config"
)

var capturedPercentiles = []float64{50, 95, 99, 99.9} // treat as const

// Aggregator collects stats across all endpoints
type Aggregator struct {
	logger          *log.Entry
	writeOutputPath string
	cancel          context.CancelFunc

	stats            map[string]*EndpointStats
	orderedEndpoints []string

	done         bool
	start        time.Time
	duration     time.Duration
	errorPercent int
	mu           sync.RWMutex
}

// EndpointStats collects stats for all vegeta workers of an endpoint
type EndpointStats struct {
	success      uint64
	errors       uint64
	errorTypes   map[string]ErrorResult
	percentiles  map[float64]time.Duration
	startRPS     float64
	targetRPS    float64
	limit        uint64 // effective per-request limit; 0 for endpoints without pagination
	startTime    time.Time     // set on activation; zero means inactive
	stepInterval time.Duration // window size for timeline snapshots
	windows      []windowStats // per-step-interval accumulators
}

// windowStats tracks metrics for a single step-interval window
type windowStats struct {
	histogram *hdrhistogram.Histogram
	success   uint64
	errors    uint64
	targetRPS float64
}

// Main driver function; consumes samples from the channel and prints progress every 5 seconds.
func (a *Aggregator) Run(ctx context.Context, in <-chan Sample) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case sample, ok := <-in:
			if !ok {
				// channel closed — engine is done
				if err := WriteOutput(a); err != nil {
					a.logger.Error(err)
				}
				return
			}
			if a.done {
				continue
			}
			if err := a.Record(sample); err != nil {
				a.logger.Error(err)
			}
		case <-ticker.C:
			a.logger.Info(a)
			if a.checkErrorPercent() > a.errorPercent {
				a.logger.Warnf("Error percentage exceeded threshold of %d%%. Ending test early.", a.errorPercent)
				a.done = true
				if err := WriteOutput(a); err != nil {
					a.logger.Error(err)
				}
				a.cancel()
				return
			}
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				a.logger.Error(fmt.Errorf("aggregator.Run terminating due to context error: %w", err))
			}
			if err := WriteOutput(a); err != nil {
				a.logger.Error(err)
			}
			return
		}
	}
}

func NewAggregator(logger *log.Entry, settings config.Config, cancel context.CancelFunc) *Aggregator {
	endpoints := settings.GetActiveEndpoints()
	duration := settings.Duration
	if settings.Serial {
		duration *= time.Duration(len(endpoints))
	}
	a := Aggregator{
		logger:          logger,
		cancel:          cancel,
		stats:           make(map[string]*EndpointStats),
		start:           time.Now(),
		duration:        duration,
		errorPercent:    settings.ErrorPercent,
		writeOutputPath: settings.TestOutputPath,
	}
	sort.Strings(endpoints)
	a.orderedEndpoints = endpoints // maintain order for consistent output

	stepInterval := max(5, settings.StepInterval)

	for _, endpointKey := range endpoints {
		a.stats[endpointKey] = &EndpointStats{
			percentiles:  make(map[float64]time.Duration),
			errorTypes:   make(map[string]ErrorResult),
			startRPS:     float64(max(settings.GetEndpointStartRPS(endpointKey), 0)),
			limit:        uint64(settings.GetEndpointLimit(endpointKey)),
			stepInterval: stepInterval,
		}
		if !settings.Serial {
			a.stats[endpointKey].startTime = time.Now()
		}
	}

	return &a
}

// mergedHistogram builds a combined histogram from all windows.
func (e *EndpointStats) mergedHistogram() *hdrhistogram.Histogram {
	merged := hdrhistogram.New(1, 60000000, 3)
	for _, w := range e.windows {
		merged.Merge(w.histogram)
	}
	return merged
}

// computes and stores percentiles from the merged window histograms
func (e *EndpointStats) refreshPercentiles() {
	merged := e.mergedHistogram()
	for _, p := range capturedPercentiles {
		e.percentiles[p] = time.Duration(merged.ValueAtPercentile(p)) * time.Microsecond
	}
}

func (a *Aggregator) Record(sample Sample) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.stats[sample.Endpoint]; !ok {
		return fmt.Errorf("unknown endpoint in sample: %s", sample.Endpoint)
	}

	epStats := a.stats[sample.Endpoint]
	if epStats.startTime.IsZero() {
		return nil
	}
	epStats.targetRPS = sample.CurrentRPS
	if sample.OK {
		epStats.success++
	} else {
		epStats.errors++
		var errKey string
		if sample.RPCErr != nil {
			errKey = sample.RPCErr.Error()
		} else if sample.Err != "" {
			errKey = sample.Err
		} else {
			errKey = strconv.Itoa(int(sample.Code))
		}

		if existing, ok := epStats.errorTypes[errKey]; ok {
			existing.Count++
			existing.LastSeen = time.Now()
			epStats.errorTypes[errKey] = existing
		} else {
			epStats.errorTypes[errKey] = newErrorResult(sample)
		}
	}
	// Bucket into per-step-interval window (windows are the source of truth for histograms)
	latencyMicros := int64(sample.Latency / time.Microsecond)
	elapsed := time.Since(epStats.startTime)
	idx := int(elapsed / epStats.stepInterval)
	for len(epStats.windows) <= idx {
		epStats.windows = append(epStats.windows, windowStats{
			histogram: hdrhistogram.New(1, 60000000, 3),
		})
	}
	w := &epStats.windows[idx]
	w.targetRPS = sample.CurrentRPS
	w.histogram.RecordValue(latencyMicros)
	if sample.OK {
		w.success++
	} else {
		w.errors++
	}

	return nil
}

func (a *Aggregator) ActivateEndpoint(endpointKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stats[endpointKey].startTime = time.Now()
}

// checkErrorPercent returns the highest error percentage of any single endpoint's
// most recently completed window. Requires at least 2 windows per endpoint so we
// never evaluate a partially-filled window.
func (a *Aggregator) checkErrorPercent() int {
	var worst int
	for _, stats := range a.stats {
		if len(stats.windows) < 2 {
			continue
		}
		w := stats.windows[len(stats.windows)-2]
		total := w.success + w.errors
		if total == 0 {
			continue
		}
		worst = max(worst, int(float64(w.errors)/float64(total)*100))
	}
	return worst
}

// constructs a logging string showing progress for all endpoints
func (a *Aggregator) String() string {
	if a.done {
		return ""
	}
	var line strings.Builder

	elapsed := time.Since(a.start).Round(time.Second)

	fmt.Fprintf(&line, "\n[%s / %s]", elapsed, a.duration)

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, endpointName := range a.orderedEndpoints {
		endpointStats := a.stats[endpointName]
		if !endpointStats.startTime.IsZero() {
			fmt.Fprintf(&line, "\n%-20s: %s", endpointName, endpointStats)
		}
	}
	line.WriteString("\n")
	if elapsed >= a.duration {
		a.done = true
	}

	return line.String()
}

// outputs a prettified one-line summary of one endpoint's stats
func (e *EndpointStats) String() string {
	e.refreshPercentiles()
	total := e.success + e.errors
	rps := e.targetRPS

	var pctOK float64
	if total > 0 {
		pctOK = float64(e.success) / float64(total) * 100
	} else {
		// if no samples yet, show start RPS to indicate starting point
		rps = e.startRPS
	}
	out := fmt.Sprintf("%6d resp (%6d ok, %4d err) %5.1f%% ok | %6.1f target RPS | ", total, e.success, e.errors, pctOK, rps)
	for _, p := range capturedPercentiles {
		out += fmt.Sprintf("p%4.1f: %8s, ", p, fmtDuration(e.percentiles[p]))
	}

	return out[:len(out)-2] // trim trailing ", "
}

func newErrorResult(sample Sample) ErrorResult {
	now := time.Now()
	return ErrorResult{
		ErrorMsg:  sample.Err,
		ErrorCode: int(sample.Code),
		Count:     1,
		FirstSeen: now,
		LastSeen:  now,
	}
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
