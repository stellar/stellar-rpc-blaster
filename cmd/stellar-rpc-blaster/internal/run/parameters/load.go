package parameters

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

type Parameters struct {
	Output seed.SeedData
	Head   HeadInfo
}

// seed fields each endpoint draws from; endpoints absent here need none.
var endpointSeedFields = map[string][]string{
	"getTransaction":   {"tx_hashes"},
	"getLedgerEntries": {"ledger_keys"},
	"getEvents":        {"contract_events", "ledger_keys"},
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

// HeadInfo is the target RPC's live ledger window, captured during preflight;
// recency-sensitive request builders anchor to it rather than the seeded range.
type HeadInfo struct {
	Oldest, Latest uint32
}

// Floor is the lowest safe startLedger: outside the left-edge margin of the
// retention floor so in-flight requests can't age out mid-run.
func (h HeadInfo) Floor() uint32 {
	return min(h.Oldest+util.LeftEdgeMargin, h.Latest)
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
