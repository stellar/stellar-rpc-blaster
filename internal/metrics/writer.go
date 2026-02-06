package blasterMetrics

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

func WriteOutput(a *Aggregator) error {
	if a.writeOutputPath != "" {
		results := a.Results()
		if writeErr := writeResultsJSON(results, a.writeOutputPath); writeErr != nil {
			return errors.Wrapf(writeErr, "Failed to write results to %s", a.writeOutputPath)
		}
		a.logger.Infof("Results written to %s", a.writeOutputPath)
		return nil
	}
	return nil
}

// WriteResultsJSON writes the results to a JSON file
func writeResultsJSON(results *Results, path string) error {
	data, err := json.Marshal(results)
	if err != nil {
		return errors.Wrap(err, "failed to marshal results")
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return errors.Wrapf(err, "failed to write results to %s", path)
	}

	return nil
}
