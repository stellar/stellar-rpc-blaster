package util

import "time"

// External configuration "facts"
const (
	CheckpointFrequency uint32 = 64
	// Maximum time to wait for a single RPC response for most endpoints
	RequestTimeout = 15 * time.Second // (this isn't accessible from outside stellar-rpc)
)

// Configurable limits for generate
const (
	TxPageLimit          uint32 = 200
	DefaultSeedSliceSize uint32 = 64 // starting size for slices of bootstrapped seed data
)

// Configurable limits for run
const (
	// HTTP constants for connection management and request timeouts
	PortCount           = 16384 // total number of ephemeral ports available on most systems
	PortAllocationRatio = 0.60  // fraction of ports to use for connections (leaves 40% free for other processes)
	WorkerMultiplier    = 2.5   // ratio for how many workers can share one connection
	MaxWorkers          = uint64(float64(PortCount) * PortAllocationRatio * WorkerMultiplier)
)

// Data dependent endpoint limits and probabilities for run
var PrKeyCount = []float64{0.8, 0.15, 0.05} // getLedgerEntries key distribution (80% 1 key, 15% [2,10], 5% [50,200])
const (
	LedgerKeyLimit         = 200
	PrJson         float64 = 0.5  // probability of using "json" vs "xdr" format for transaction requests
	PrCursor       float64 = 0.5  // probability of paginating with a cursor
	PrTxFound      float64 = 0.70 // probability of a transaction hash found vs a random hash (won't be found)
)

// TxData status values
const (
	TxSuccess  string = "SUCCESS"
	TxFailed   string = "FAILED"
	TxNotFound string = "NOT_FOUND"
)
