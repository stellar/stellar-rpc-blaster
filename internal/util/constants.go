package util

import "time"

const (
	CheckpointFrequency = 64

	// HTTP constants for connection management and request timeouts
	PortCount           = 16384 // total number of ephemeral ports available on most systems
	PortAllocationRatio = 0.60  // fraction of ports to use for connections (leaves 40% free for other processes)
	WorkerMultiplier    = 2.5   // ratio for how many workers can share one connection
	MaxWorkers          = uint64(float64(PortCount) * PortAllocationRatio * WorkerMultiplier)

	// Maximum time to wait for a single RPC response
	// (for most endpoints -- this isn't accessible from outside stellar-rpc)
	RequestTimeout = 15 * time.Second

	TxPageLimit uint32 = 200
)
