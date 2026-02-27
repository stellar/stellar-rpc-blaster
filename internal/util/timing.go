package util

import (
	"fmt"
	"time"
)

// For convenience and consistency in logging elapsed time across the repo
// Returns a string of format "(elapsed: [1.023]s)"
func LogElapsed(start time.Time) string {
	return fmt.Sprintf("(elapsed: %s)", time.Since(start).Round(time.Millisecond).String())
}
