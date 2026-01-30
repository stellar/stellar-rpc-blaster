package main

import (
	"fmt"
	"os"

	"github.com/stellar/stellar-rpc-blaster/cmd"
)

// At a high level, the main function calls cmd.Execute, which creates the commands
// and runs them through the App struct in internal/app.go.
// This calls the engine to run the load tests (internal/engine/{vegeta.go -> run.go -> blast.go}).
// Metrics are collected and reported through internal/metrics [TODO].
func main() {
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
