package seed

import (
	"context"
	"fmt"
	"slices"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	checkpoint "github.com/stellar/go-stellar-sdk/historyarchive"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
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
		return 0, errors.Wrap(err, "failed to fetch latest ledger")
	}

	return checkpointManager.PrevCheckpoint(latestLedger.Sequence), nil
}

// Parses raw CLI ledgerWindow value read in into a Range
func GetLedgerRange(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	ledgerWindow []uint32,
	count uint32,
) (Range, error) {
	latestCheckpointLedger, err := GetLatestCheckpointLedger(ctx, rpcClient)
	if err != nil {
		return Range{}, errors.Wrap(err, "failed to get latest checkpoint ledger")
	}

	var first, last uint32
	switch len(ledgerWindow) {
	case 0:
		// No window specified: [LatestCheckpoint - count, LatestCheckpoint]
		if count > latestCheckpointLedger {
			return Range{}, errors.Errorf("count (%d) exceeds latest checkpoint ledger (%d)", count, latestCheckpointLedger)
		}
		first = latestCheckpointLedger - count + 1
		last = latestCheckpointLedger
	case 1:
		// Only START: [START, LatestCheckpoint]
		first = ledgerWindow[0]
		last = latestCheckpointLedger
	case 2:
		// Both START and END: [START, END]
		first = ledgerWindow[0]
		last = ledgerWindow[1]
	default:
		return Range{}, errors.Errorf("ledger-window must have at most 2 values, got %d", len(ledgerWindow))
	}

	if first > last {
		return Range{}, errors.Errorf("invalid ledger range: start (%d) > end (%d)", first, last)
	}

	return Range{First: first, Last: last}, nil
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
	slices.SortFunc(ledgers, func(a, b uint32) int { return int(a) - int(b) })

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
		return []uint32{window.First}, nil
	}

	sampled := make([]uint32, count)
	step := float64(window.Last-window.First) / float64(count-1)
	for i := range count {
		sampled[i] = window.First + uint32(float64(i)*step+0.5)
	}
	return sampled, nil
}
