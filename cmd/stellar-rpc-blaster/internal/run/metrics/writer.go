package blasterMetrics

import (
	"fmt"
	"os"
	"path/filepath"
)

func WriteOutput(a *Aggregator) error {
	if a.writeOutputPath != "" {
		results := a.Results()
		if writeErr := writeResultsJSON(results, a.writeOutputPath); writeErr != nil {
			return fmt.Errorf("Failed to write results to %s: %w", a.writeOutputPath, writeErr)
		}
		a.logger.Infof("Results written to %s", a.writeOutputPath)
		return nil
	}
	return nil
}

// writeResultsJSON writes the results to a JSON file
func writeResultsJSON(results *Results, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create output directories for path %s: %w", path, err)
	}

	data, err := results.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal results: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write results to %s: %w", path, err)
	}

	return nil
}
