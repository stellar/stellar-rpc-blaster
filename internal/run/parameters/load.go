package parameters

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
)

type Parameters struct {
	Output       seed.SeedData
	SampleCounts map[string]int
}

func GetParameters(dataPath string) (*Parameters, error) {
	output, err := loadParameters(dataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load parameters: %v", err)
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
		return seed.SeedData{}, fmt.Errorf("couldn't open seed data file %s: %v", dataPath, err)
	}
	defer f.Close()

	var output seed.SeedData
	if err := json.NewDecoder(f).Decode(&output); err != nil {
		return seed.SeedData{}, fmt.Errorf("failed to decode seed data: %v", err)
	}
	return output, nil
}

func (w *Parameters) fillCounts() {
	w.SampleCounts = map[string]int{
		"tx_hashes":    len(w.Output.TxHashes),
		"contract_ids": len(w.Output.ContractIds),
		"event_topics": len(w.Output.EventTopics),
		"ledger_keys":  len(w.Output.LedgerKeys),
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
