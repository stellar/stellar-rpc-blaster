package seed

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

type OutputSchema struct {
	LedgerRange Range    `json:"ledger_range"`
	TxHashes    []TxData `json:"tx_hashes"`
	ContractIDs []string `json:"contract_ids"`
	EventTopics []string `json:"event_topics"`
	LedgerKeys  []string `json:"ledger_keys"`
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

// Slice returns the accumulated items from an Entry.
func (e *Entry[T]) Slice() []T {
	return e.Map[e.key].([]T)
}

// buffer for accumulating items under a JSON key
type Entry[T any] struct {
	Map map[string]any
	key string
}

// NewEntry creates a new write buffer with the given JSON key and element type
func NewEntry[T any](key string, cap int) Entry[T] {
	m := make(map[string]any)
	m[key] = make([]T, 0, cap)
	return Entry[T]{Map: m, key: key}
}

// Append adds an item to the entry's slice
func (e *Entry[T]) Append(item T) {
	e.Map[e.key] = append(e.Map[e.key].([]T), item)
}
