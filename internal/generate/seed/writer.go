package seed

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

// SeedWriter streams JSON output as {"key1":[...], "key2":[...], ...}
// Open once in Generate(), pass to each seed function, close at the end.
type SeedWriter struct {
	encoder *json.Encoder
	output  map[string]any
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
		output:  make(map[string]any),
	}, nil
}

// Flushes m into the writer's output final output buffer
func (w *SeedWriter) FlushMap(m map[string]any) error {
	for k, v := range m {
		w.output[k] = v
	}
	return nil
}

// Flush encodes the accumulated output as one JSON object and writes it.
func (w *SeedWriter) Flush() error {
	if err := w.encoder.Encode(w.output); err != nil {
		return errors.Wrap(err, "failed to encode output")
	}
	return nil
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
