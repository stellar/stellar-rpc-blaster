package blasterMetrics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	types "github.com/stellar/stellar-rpc-blaster/internal/config"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/stellar/go-stellar-sdk/support/log"
)

var capturedPercentiles = []float64{50, 95, 99, 99.9} // treat as const

// Aggregator collects stats across all endpoints
type Aggregator struct {
	logger           *log.Entry
	mu               sync.RWMutex
	stats            map[string]*EndpointStats
	orderedEndpoints []string
	start            time.Time
	duration         time.Duration
}

// EndpointStats collects stats for all clients of an endpoint
type EndpointStats struct {
	clients     []ClientStats
	success     uint64
	errors      uint64
	percentiles map[float64]time.Duration
	currentRPS  int
}

// ClientStats tracks metrics for a single client of a single endpoint using HDR Histogram
type ClientStats struct {
	histogram   *hdrhistogram.Histogram
	success     uint64
	errors      uint64
	percentiles map[float64]time.Duration
}

func NewAggregator(logger *log.Entry, settings types.LoadTestSettings) *Aggregator {
	a := Aggregator{
		logger:   logger,
		stats:    make(map[string]*EndpointStats),
		start:    time.Now(),
		duration: settings.GetDuration(),
	}
	endpoints := settings.GetEndpoints() // maps.Keys(a.stats))
	sort.Strings(endpoints)
	a.orderedEndpoints = endpoints // maintain order for consistent output

	for _, endpointKey := range endpoints {
		_, numClients := settings.GetEndpoint(endpointKey)
		a.stats[endpointKey] = newEndpointStats(numClients)
	}
	return &a
}

func newEndpointStats(numClients int) *EndpointStats {
	clients := make([]ClientStats, numClients)
	for i := range numClients {
		clients[i] = ClientStats{
			histogram:   hdrhistogram.New(1, 60000000, 3),
			percentiles: make(map[float64]time.Duration),
		}
	}
	return &EndpointStats{
		clients: clients,
	}
}

// Aggregate current stats across all clients for one endpoint
func (e *EndpointStats) RefreshEndpointStats() {
	var success, errors uint64
	e.percentiles = make(map[float64]time.Duration)
	// Capture client's stats first to ensure all snapshots are taken at the same time
	for _, c := range e.clients {
		c.snapClientState()
	}
	for i := range e.clients {
		success += e.clients[i].success
		errors += e.clients[i].errors
		for _, p := range capturedPercentiles {
			e.percentiles[p] += e.clients[i].percentiles[p]
		}
	}
	for _, p := range capturedPercentiles {
		e.percentiles[p] /= time.Duration(len(e.clients))
	}
	e.success = success
	e.errors = errors
}

func (e *EndpointStats) outputStats() string {
	total := e.success + e.errors
	out := fmt.Sprintf("%6d req (%6d ok, %4d err) | %5d RPS | ", total, e.success, e.errors, e.currentRPS)
	for _, p := range capturedPercentiles {
		out += fmt.Sprintf("p%4.1f: %8s, ", p, fmtDuration(e.percentiles[p]))
	}
	return out[:len(out)-2] // trim trailing ", "
}

func (c *ClientStats) snapClientState() {
	for _, p := range capturedPercentiles {
		c.percentiles[p] = time.Duration(c.histogram.ValueAtPercentile(p)) * time.Microsecond
	}
}

func (a *Aggregator) Record(sample Sample) {
	epStats := a.stats[sample.Endpoint]
	clientStats := &epStats.clients[sample.ClientId]
	if sample.OK {
		clientStats.success++
	} else {
		clientStats.errors++
	}
	clientStats.histogram.RecordValue(int64(sample.Latency / time.Microsecond))
	epStats.currentRPS = max(epStats.currentRPS, sample.CurrentRPS)
}

// Run consumes samples from the channel and prints progress every 5 seconds
func (a *Aggregator) Run(ctx context.Context, in <-chan Sample) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case sample, ok := <-in:
			if !ok {
				return // channel closed
			}
			a.Record(sample)
		case <-ticker.C:
			a.logger.Info(a.makeProgressString())
		case <-ctx.Done():
			return
		}
	}
}

func (a *Aggregator) makeProgressString() string {
	var line strings.Builder

	elapsed := time.Since(a.start).Round(time.Second)

	fmt.Fprintf(&line, "\n[%s / %s]", elapsed, a.duration)

	// endpoints := slices.Sorted(maps.Keys(a.stats))
	a.mu.RLock()
	defer a.mu.RUnlock()

	for _, endpointName := range a.orderedEndpoints {
		endpointStats := a.stats[endpointName]
		endpointStats.RefreshEndpointStats()
		fmt.Fprintf(&line, "\n%-20s: %s", endpointName, endpointStats.outputStats())
	}

	if elapsed >= a.duration {
		return "=== Final Results ===" + line.String()
	}
	return line.String()
}

func fmtDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%4dµs", d.Microseconds())
	} else if d < time.Second {
		return fmt.Sprintf("%4.1fms", float64(d.Microseconds())/1e3)
	}
	return fmt.Sprintf("%4.1fs", float64(d.Milliseconds())/1e3)
}
