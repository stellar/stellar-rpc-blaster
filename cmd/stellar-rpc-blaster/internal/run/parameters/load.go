package parameters

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

type Parameters struct {
	Output  seed.SeedData
	Head    HeadInfo
	RngSeed uint64
}

// seed fields each endpoint draws from; endpoints absent here need none.
var endpointSeedFields = map[string][]string{
	"getTransaction":   {"tx_hashes"},
	"getLedgerEntries": {"ledger_keys"},
	"getEvents":        {"contract_events", "ledger_keys"},
	"queryEvents":      {"contract_events", "ledger_keys"},
}

// missingSeedFields returns the seed fields the given endpoints need but the loaded
// seed data lacks, so runs only require the data they'll actually draw from.
func (p *Parameters) missingSeedFields(endpointKeys []string) []string {
	counts := map[string]int{
		"tx_hashes":       len(p.Output.TxHashes),
		"ledger_keys":     len(p.Output.LedgerKeys),
		"contract_events": len(p.Output.ContractEventData.ContractIds),
	}
	missing := map[string]bool{}
	for _, key := range endpointKeys {
		for _, field := range endpointSeedFields[key] {
			if counts[field] == 0 {
				missing[field] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(missing))
}

// HeadInfo is the target RPC's live ledger window, captured during preflight and
// narrowed to any query window; recency-sensitive request builders anchor to it
// rather than the seeded range.
type HeadInfo struct {
	Oldest, Latest uint32
	Start          uint32 // query window start, raising Floor (0: none)
	Drift          uint32 // ledgers the retention floor will pass during the run, widening Margin (0: none)
	Pinned         bool   // Latest is a query window's END rather than the live head
}

// Margin is how many ledgers above Oldest stay clear of placements: request bodies
// are built at startup, so anything the retention floor passes during the run would
// age out before being sent. At least LeftEdgeMargin, or the run's expected Drift.
func (h HeadInfo) Margin() uint32 {
	return max(util.LeftEdgeMargin, h.Drift)
}

// Floor is the lowest safe startLedger: Margin above the retention floor, and no
// lower than the query window's start.
func (h HeadInfo) Floor() uint32 {
	return min(max(h.Oldest+h.Margin(), h.Start), h.Latest)
}

// DriftLedgers is how many ledgers the retention floor is expected to pass in a run
// of the given length, with half again as headroom for faster-than-nominal closes.
func DriftLedgers(runLength time.Duration) uint32 {
	return uint32(3 * runLength / (2 * util.LedgerCloseTime))
}

// Narrow clamps a --query-window into the queryable range [Floor, Latest]: START or
// START,END as absolute ledgers, or -N for the newest N. An END stands in for the
// live head.
func (h *HeadInfo) Narrow(window []int64) error {
	if len(window) == 0 {
		return nil
	}
	start, end := window[0], int64(h.Latest)
	if start < 0 { // -N: the newest N ledgers
		start += int64(h.Latest) + 1
	}
	if len(window) == 2 {
		end = window[1]
	}
	lo, hi := max(start, int64(h.Floor())), min(end, int64(h.Latest))
	if lo > hi {
		return fmt.Errorf("query-window [%d, %d] has no ledgers in the target's queryable range [%d, %d]",
			start, end, h.Floor(), h.Latest)
	}
	h.Start, h.Latest, h.Pinned = uint32(lo), uint32(hi), len(window) == 2
	return nil
}

// Clamp bounds a start into [Floor, Latest]; small retention windows degrade
// deep placements toward the floor rather than erroring.
func (h HeadInfo) Clamp(start uint32) uint32 {
	return min(max(start, h.Floor()), h.Latest)
}

// Back returns the startLedger depth ledgers behind head capped at the floor.
func (h HeadInfo) Back(depth uint32) uint32 {
	return h.Latest - min(depth, h.Latest-h.Floor())
}

// Warnings returns advisory conditions that degrade the configured endpoints'
// traffic realism without blocking the run.
func (p *Parameters) Warnings(endpointKeys []string) []string {
	var warnings []string
	if slices.ContainsFunc(endpointKeys, HasArchetypes) && len(collectWallets(p.Output.ContractEventData)) == 0 {
		warnings = append(warnings, "seed data has no account-address event params; transfer-watcher bodies degrade to head-polls")
	}
	return warnings
}

func GetParameters(dataPath string) (*Parameters, error) {
	output, err := loadParameters(dataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load parameters: %w", err)
	}
	return &Parameters{Output: output}, nil
}

func loadParameters(dataPath string) (seed.SeedData, error) {
	f, err := os.Open(dataPath)
	if err != nil {
		return seed.SeedData{}, fmt.Errorf("couldn't open seed data file %s: %w", dataPath, err)
	}
	defer f.Close()

	var output seed.SeedData
	if err := json.NewDecoder(f).Decode(&output); err != nil {
		return seed.SeedData{}, fmt.Errorf("failed to decode seed data: %w", err)
	}
	return output, nil
}
