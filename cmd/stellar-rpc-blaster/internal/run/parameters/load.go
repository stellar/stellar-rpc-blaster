package parameters

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

type Parameters struct {
	Output       seed.SeedData
	SampleCounts map[string]int
	Head         HeadInfo
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

func GetParameters(dataPath string) (*Parameters, error) {
	output, err := loadParameters(dataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load parameters: %w", err)
	}

	params := &Parameters{
		Output: output,
	}
	params.fillCounts()

	return params, params.Validate()
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

func (w *Parameters) fillCounts() {
	w.SampleCounts = map[string]int{
		"tx_hashes":       len(w.Output.TxHashes),
		"ledger_keys":     len(w.Output.LedgerKeys),
		"contract_events": len(w.Output.ContractEventData.ContractIds),
	}
}

func (w *Parameters) Validate() error {
	missingFields := []string{}
	for key, count := range w.SampleCounts {
		if count == 0 {
			missingFields = append(missingFields, key)
		}
	}
	if len(missingFields) > 0 {
		return fmt.Errorf("sample counts for the following fields are zero: %s", strings.Join(missingFields, ", "))
	}
	return nil
}
