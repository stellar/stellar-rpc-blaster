package blasterMetrics

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

// WriteResultsJSON writes the results to a JSON file
func WriteResultsJSON(results *Results, path string) error {
	data, err := json.Marshal(results)
	if err != nil {
		return errors.Wrap(err, "failed to marshal results")
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return errors.Wrapf(err, "failed to write results to %s", path)
	}

	return nil
}
