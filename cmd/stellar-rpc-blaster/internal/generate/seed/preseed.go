package seed

import (
	"context"
	"fmt"
	"slices"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	checkpoint "github.com/stellar/go-stellar-sdk/historyarchive"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

type PreseedParameters struct {
	ExportPath string
	Range      Range `json:"ledger_range"`
	// When nil, the full Range is used.
	ProcessingRanges []Range // holds the sub-ranges to actually seed data from when window > count
}

type Range struct {
	First uint32 `json:"first"`
	Last  uint32 `json:"last"`
}

func GetLatestCheckpointLedger(ctx context.Context, rpcClient *rpcclient.Client) (uint32, error) {
	checkpointManager := checkpoint.NewCheckpointManager(util.CheckpointFrequency)
	latestLedger, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch latest ledger: %w", err)
	}

	return checkpointManager.PrevCheckpoint(latestLedger.Sequence), nil
}

// GetLedgerRange resolves a --ledger-window against the latest checkpoint: START or
// START,END as absolute ledgers, -N for the newest N, or the newest count when unset.
func GetLedgerRange(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	ledgerWindow []int64,
	count uint32,
) (Range, error) {
	latestCheckpointLedger, err := GetLatestCheckpointLedger(ctx, rpcClient)
	if err != nil {
		return Range{}, fmt.Errorf("failed to get latest checkpoint ledger: %w", err)
	}
	ckpt := int64(latestCheckpointLedger)

	first, last := ckpt-int64(count)+1, ckpt
	switch len(ledgerWindow) {
	case 0:
	case 1:
		first = ledgerWindow[0]
	case 2:
		first, last = ledgerWindow[0], ledgerWindow[1]
	default:
		return Range{}, fmt.Errorf("ledger-window must have at most 2 values, got %d", len(ledgerWindow))
	}
	if first < 0 { // -N: the newest N ledgers
		first += ckpt + 1
	}
	if first < 1 || first > last {
		return Range{}, fmt.Errorf("ledger-window resolves to [%d, %d], not a valid ledger range (latest checkpoint: %d)", first, last, ckpt)
	}
	return Range{First: uint32(first), Last: uint32(last)}, nil
}

// This returns the range(s) to iterate over during seeding
func (p PreseedParameters) GetProcessingRanges() []Range {
	if len(p.ProcessingRanges) > 0 {
		return p.ProcessingRanges
	}
	return []Range{p.Range}
}

// Group list of sampled ledgers into contiguous ranges to minimize number of RPC calls during seeding
func GroupSampledLedgersIntoRanges(ledgers []uint32) []Range {
	if len(ledgers) == 0 {
		return nil
	}
	slices.SortFunc(ledgers,
		func(a, b uint32) int {
			return int(a) - int(b)
		},
	)

	ranges := []Range{{First: ledgers[0], Last: ledgers[0]}}
	for _, l := range ledgers[1:] {
		curr := &ranges[len(ranges)-1]
		if l == curr.Last+1 {
			curr.Last = l
		} else {
			ranges = append(ranges, Range{First: l, Last: l})
		}
	}
	return ranges
}

// Returns count uniformly-spaced ledger sequence numbers
// in [first, last]
func ComputeSampledLedgers(window Range, count uint32) ([]uint32, error) {
	windowSize := window.Last - window.First + 1
	if windowSize == 0 {
		return nil, fmt.Errorf("ledger window to sample from cannot be empty")
	}
	if count >= windowSize {
		return nil, fmt.Errorf("count of ledgers to sample cannot be smaller than the length of the ledger window")
	}
	if count <= 1 {
		return nil, fmt.Errorf("must request ledger sample size of at least 2")
	}

	sampled := make([]uint32, count)
	step := float64(window.Last-window.First) / float64(count-1)
	for i := range count {
		sampled[i] = window.First + uint32(float64(i)*step+0.5)
	}
	return sampled, nil
}
