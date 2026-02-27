package writer

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
)

// SeedWriter accumulates seed data into an OutputSchema struct,
// then encodes it as a single ordered JSON object on Flush().
type SeedWriter struct {
	file *os.File
	seed.SeedData
}

func NewSeedWriter(path string) (*SeedWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create seed file %s: %v", path, err)
	}
	return &SeedWriter{
		file: f,
	}, nil
}

func (w *SeedWriter) MarshalJSON() ([]byte, error) {
	return json.MarshalIndent(w.SeedData, "", "  ")
}

func (w *SeedWriter) Write(bytes []byte) (int, error) {
	return w.file.Write(bytes)
}

func (w *SeedWriter) Close() (err error) {
	defer func() {
		if closeErr := w.file.Close(); closeErr != nil {
			err = closeErr
		}
	}()

	data, err := w.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal seed data to JSON: %v", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write seed data to file: %v", err)
	}
	return
}
