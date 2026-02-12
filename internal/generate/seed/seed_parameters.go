package seed

import (
	"slices"
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

func WriteLedgerRangeEntry(params PreseedParameters, writer *SeedWriter) error {
	return writer.FlushMap(map[string]any{
		"ledger_range": params.Range,
	})
}
