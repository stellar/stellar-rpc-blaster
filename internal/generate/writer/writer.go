package writer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
)

var (
	_ json.Marshaler = (*SeedWriter)(nil)
	_ io.WriteCloser = (*SeedWriter)(nil)
)

// SeedWriter accumulates seed data into an OutputSchema struct,
// then encodes it as a single ordered JSON object on Flush().
type SeedWriter struct {
	io.WriteCloser
	seed.SeedData
}

func NewSeedWriter(path string) (*SeedWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create seed file %s: %v", path, err)
	}
	return &SeedWriter{
		WriteCloser: f,
	}, nil
}

func (w *SeedWriter) MarshalJSON() ([]byte, error) {
	return json.MarshalIndent(w.SeedData, "", "  ")
}

func (w *SeedWriter) Write(bytes []byte) (int, error) {
	return w.WriteCloser.Write(bytes)
}

func (w *SeedWriter) Close() error {
	data, err := w.MarshalJSON()
	if err != nil {
		return errors.Join(fmt.Errorf("failed to marshal seed data to JSON: %v", err), w.WriteCloser.Close())
	}
	if _, err := w.Write(data); err != nil {
		return errors.Join(fmt.Errorf("failed to write seed data to file: %v", err), w.WriteCloser.Close())
	}
	return w.WriteCloser.Close()
}
