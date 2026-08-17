package util

import "time"

// External configuration "facts"
const (
	CheckpointFrequency uint32 = 64
	// Maximum time to wait for a single RPC response for most endpoints
	RequestTimeout = 15 * time.Second // (this isn't accessible from outside stellar-rpc)

	// Hard-coded RPC endpoint pagination limits
	MaxTxPageLimit      uint32 = 200
	MaxLedgersPageLimit uint32 = 200
	MaxEventsPageLimit  uint32 = 10000
)

// Configurable limits for generate
const (
	// Default pagination limits that occur in RPC when pagination is used without a limit
	DefaultTxPageLimit     uint32 = 50
	DefaultEventsPageLimit uint32 = 100

	DefaultSeedSliceSize uint32 = 64 // starting size for slices of bootstrapped seed data

	// Caps on stored event seed data (counts keep accumulating past them)
	MaxSeedEventContracts    = 20 // top emitter contracts kept, by observed emission count
	MaxSeedTopicsPerContract = 8
	MaxSeedParamSetsPerTopic = 5
)

// Configurable limits for run
const (
	// HTTP constants for connection management and request timeouts
	PortCount           = 16384 // total number of ephemeral ports available on most systems
	PortAllocationRatio = 0.60  // fraction of ports to use for connections (leaves 40% free for other processes)
	WorkerMultiplier    = 2.5   // ratio for how many workers can share one connection
	MaxWorkers          = uint64(float64(PortCount) * PortAllocationRatio * WorkerMultiplier)

	// Max number of request bodies to pre-generate for data-dependent endpoints
	MaxNumPrebuiltBodies = int(100000)
)

// Data dependent endpoint limits and probabilities for run
var PrKeyCount = []float64{0.8, 0.15, 0.05} // getLedgerEntries key distribution (80% 1 key, 15% [2,10], 5% [50,200])
const (
	LedgerKeyLimit         = 200
	PrJson         float64 = 0.5 // probability of using "json" vs "xdr" format for transaction requests
)

// Traffic models measured from a one-week production capture re-joined uncapped
// against full ledger history (the getEvents archetype mixture lives in
// run/parameters/events_archetypes.go; the rest inline in endpoints.go)
const (
	TrafficProfileVersion        = 2     // stamped into results JSON; bump when any endpoint model changes
	PrEventsJson         float64 = 0.015 // xdrFormat "json"; the key is omitted otherwise (base64 default)
	EventsLeftEdgeMargin uint32  = 1000  // never place startLedger within this of the retention floor
	EventsDeepBandFloor  uint32  = 10000 // "deep" placement means at least this far behind head
	EventsColdPoolSize           = 50    // deployed-but-quiet contracts drawn from seed ledger keys

	PrTxRepoll        float64 = 0.6  // getTransaction requests re-polling a recently polled hash (measured 50-70%)
	PrTxNotFound      float64 = 0.12 // fresh polls targeting hashes that never land (7% never-land + 5.2% pre-landing)
	TxRecentWindow            = 8    // recently-polled hashes eligible for repoll
	PrTxsNearHead     float64 = 0.98 // getTransactions starts within 1k ledgers of head
	PrLedgersNearHead float64 = 0.65 // getLedgers starts within 1k ledgers of head
)
