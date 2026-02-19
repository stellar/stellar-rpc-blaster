package parameters

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/pkg/errors"

	"github.com/stellar/stellar-rpc-blaster/internal/generate/writer"
)

type Parameters struct {
	Output       writer.OutputSchema
	SampleCounts map[string]int
}

func GetParameters(dataPath string) (*Parameters, error) {
	output, err := loadParameters(dataPath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load parameters")
	}

	params := &Parameters{
		Output: output,
	}
	params.fillCounts()

	return params, params.Validate()
}

func loadParameters(dataPath string) (writer.OutputSchema, error) {
	f, err := os.Open(dataPath)
	if err != nil {
		return writer.OutputSchema{}, errors.Wrapf(err, "couldn't open seed data file %s", dataPath)
	}
	defer f.Close()

	var output writer.OutputSchema
	if err := json.NewDecoder(f).Decode(&output); err != nil {
		return writer.OutputSchema{}, errors.Wrap(err, "failed to decode seed data")
	}
	return output, nil
}

func (w *Parameters) fillCounts() {
	w.SampleCounts = map[string]int{
		"tx_hashes":    len(w.Output.TxHashes.Successes) + len(w.Output.TxHashes.Failures),
		"contract_ids": len(w.Output.ContractIDs),
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
		return errors.New("sample counts for the following fields are zero: " + strings.Join(missingFields, ", "))
	}
	return nil
}
