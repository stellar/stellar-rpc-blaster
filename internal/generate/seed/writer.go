package seed

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pkg/errors"
)

// SeedWriter streams JSON output as {"key1":[...], "key2":[...], ...}
// Open once in Generate(), pass to each seed function, close at the end.
type SeedWriter struct {
	file      *os.File
	hasField  bool // whether we've written at least one top-level field
	inArray   bool // whether we're currently inside an array
	arraySize int  // items written in current array
}

func NewSeedWriter(path string) (*SeedWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create seed file %s", path)
	}
	if _, err := f.WriteString("{"); err != nil {
		f.Close()
		return nil, errors.Wrap(err, "failed to write opening brace")
	}
	return &SeedWriter{
		file: f,
	}, nil
}

// StartArray begins a new top-level array field: "key": [
func (w *SeedWriter) StartArray(key string) error {
	if w.inArray {
		return errors.New("cannot start array: previous array not ended")
	}
	prefix := ""
	if w.hasField {
		prefix = ","
	}
	if _, err := fmt.Fprintf(w.file, "%s\"%s\":[", prefix, key); err != nil {
		return errors.Wrapf(err, "failed to start array %s", key)
	}
	w.inArray = true
	w.arraySize = 0
	w.hasField = true
	return nil
}

// WriteItem appends a single item to the current array.
func (w *SeedWriter) WriteItem(v any) error {
	if !w.inArray {
		return errors.New("cannot write item: no array started")
	}
	if w.arraySize > 0 {
		if _, err := w.file.WriteString(",\n"); err != nil {
			return errors.Wrap(err, "failed to write comma")
		}
	}
	if vBytes, err := json.MarshalIndent(v, "", "  "); err != nil {
		return errors.Wrap(err, "failed to marshal item")
	} else {
		if _, err := w.file.Write(vBytes); err != nil {
			return errors.Wrap(err, "failed to write item")
		}
	}
	w.arraySize++
	return nil
}

// EndArray closes the current array: ]
func (w *SeedWriter) EndArray() error {
	if !w.inArray {
		return errors.New("cannot end array: no array started")
	}
	if _, err := w.file.WriteString("]\n"); err != nil {
		return errors.Wrap(err, "failed to close array")
	}
	w.inArray = false
	return nil
}

// Close writes the closing } and closes the file.
func (w *SeedWriter) Close() error {
	if w.inArray {
		if err := w.EndArray(); err != nil {
			return err
		}
	}
	if _, err := w.file.WriteString("}"); err != nil {
		w.file.Close()
		return errors.Wrap(err, "failed to write closing brace")
	}
	return w.file.Close()
}
