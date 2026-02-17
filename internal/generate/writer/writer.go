package writer

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type OutputSchema struct {
	LedgerRange util.Range      `json:"ledger_range"`
	TxHashes    seed.TxHashData `json:"tx_hashes"`
	ContractIDs []string        `json:"contract_ids"`
	EventTopics []string        `json:"event_topics"`
	LedgerKeys  []string        `json:"ledger_keys"`
}

// SeedWriter accumulates seed data into an OutputSchema struct,
// then encodes it as a single ordered JSON object on Flush().
type SeedWriter struct {
	encoder *json.Encoder
	Output  OutputSchema
}

func NewSeedWriter(path string) (*SeedWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create seed file %s", path)
	}
	e := json.NewEncoder(f)
	e.SetIndent("", "  ")
	return &SeedWriter{
		encoder: e,
		Output:  OutputSchema{},
	}, nil
}

// Flush encodes the accumulated output as one JSON object and writes it.
func (w *SeedWriter) Flush() error {
	if err := w.encoder.Encode(w.Output); err != nil {
		return errors.Wrap(err, "failed to encode output")
	}
	return nil
}
