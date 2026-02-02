package blasterMetrics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/stellar/go-stellar-sdk/support/log"
)

var capturedPercentiles = []float64{50, 90, 95, 99, 99.9} // treat as const

// Aggregator collects stats across all endpoints
type Aggregator struct {
	logger   *log.Entry
	mu       sync.RWMutex
	stats    map[string]*EndpointStats
	start    time.Time
	duration time.Duration
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

func NewAggregator(logger *log.Entry, duration time.Duration, endpointToClient map[string]int) *Aggregator {
	a := Aggregator{
		logger:   logger,
		stats:    make(map[string]*EndpointStats),
		start:    time.Now(),
		duration: duration,
	}
	for endpoint, numClients := range endpointToClient {
		a.stats[endpoint] = newEndpointStats(numClients)
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
	out := fmt.Sprintf("%d req (%d ok, %d err) | %d RPS (peak) | ", total, e.success, e.errors, e.currentRPS)
	for _, p := range capturedPercentiles {
		out += fmt.Sprintf("p%.1f: %s, ", p, fmtDuration(e.percentiles[p]))
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
				return
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
	a.mu.RLock()
	defer a.mu.RUnlock()

	elapsed := time.Since(a.start).Round(time.Second)
	fmt.Fprintf(&line, "\n[%s / %s]", elapsed, a.duration)

	for endpointName, endpointStats := range a.stats {
		endpointStats.RefreshEndpointStats()
		fmt.Fprintf(&line, "\n%s: %s", endpointName, endpointStats.outputStats())
	}
	return line.String()
}

func (a *Aggregator) PrintFinal() {
	a.logger.Info("=== Final Results ===\n" + a.makeProgressString())
}

func fmtDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%.2fms", float64(d.Milliseconds())+float64(d.Microseconds()%1000)/1000)
}
