package main

import (
	"fmt"
	"os"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/cli"
)

// Calls cli.Execute, which creates the commands and runs them through the App struct in internal/app.go.
// This calls the engine to run the load tests or generate seed data.
func main() {
	err := cli.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
