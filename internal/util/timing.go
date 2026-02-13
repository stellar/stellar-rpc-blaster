package util

import (
	"fmt"
	"time"
)

// For convenience and consistency in logging elapsed time across the repo
func LogElapsed(start time.Time) string {
	return fmt.Sprintf("(elapsed: %s)", time.Since(start).Round(time.Millisecond).String())
}
