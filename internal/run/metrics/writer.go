package blasterMetrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func WriteOutput(a *Aggregator) error {
	if a.writeOutputPath != "" {
		results := a.Results()
		if writeErr := writeResultsJSON(results, a.writeOutputPath); writeErr != nil {
			return fmt.Errorf("Failed to write results to %s: %v", a.writeOutputPath, writeErr)
		}
		a.logger.Infof("Results written to %s", a.writeOutputPath)
		return nil
	}
	return nil
}

// WriteResultsJSON writes the results to a JSON file
func writeResultsJSON(results *Results, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create output directories for path %s: %v", path, err)
	}

	data, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("failed to marshal results: %v", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write results to %s: %v", path, err)
	}

	return nil
}
