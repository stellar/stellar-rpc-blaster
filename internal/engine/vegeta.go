package engine

import (
	"net/http"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type BlasterOptions struct {
	Timeout   time.Duration
	KeepAlive bool
	// these are expected to be ints by net/http
	MaxConnsPerHost     int
	MaxIdleConns        int
	MaxIdleConnsPerHost int
}

// Creates an HTTP client for our blasters/Vegeta attackers
func NewHTTPClient(opts BlasterOptions) *http.Client {
	tr := http.Transport{
		DisableKeepAlives:   !opts.KeepAlive,
		MaxConnsPerHost:     opts.MaxConnsPerHost,
		MaxIdleConns:        opts.MaxIdleConns,
		MaxIdleConnsPerHost: opts.MaxIdleConnsPerHost,
	}
	return &http.Client{
		Transport: &tr,
		Timeout:   opts.Timeout,
	}
}

// Creates a new blaster/Vegeta attacker given a (potentially shared) HTTP client
// The Vegeta attacker is what actually performs the load testing
func NewBlaster(client *http.Client) *vegeta.Attacker {
	return vegeta.NewAttacker(
		vegeta.Client(client),
	)
}
