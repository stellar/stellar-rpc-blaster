package writer

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
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
		return nil, errors.Wrapf(err, "failed to create seed file %s", path)
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
		return errors.Wrap(err, "failed to marshal seed data to JSON")
	}

	if _, err := w.Write(data); err != nil {
		return errors.Wrap(err, "failed to write seed data to file")
	}
	return
}
