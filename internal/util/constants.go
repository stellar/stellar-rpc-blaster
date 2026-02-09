package util

import "time"

const (
	CheckpointFrequency = 64

	GetLatestLedgerMethodName = "getLatestLedger"

	// Total number of ephemeral ports available on most systems
	PortCount = 16384

	// Fraction of available ephemeral ports to use for connections (leaves 40% free for other processes)
	PortAllocationRatio = 0.60

	// Ratio determining how many workers can share one connection
	WorkerMultiplier = 2.5

	// Maximum time to wait for a single RPC response
	// (for most endpoints -- this isn't accessible from outside stellar-rpc)
	RequestTimeout = 15 * time.Second

	MaxWorkers = uint64(float64(PortCount) * PortAllocationRatio * WorkerMultiplier)
)
