package util

import (
	"net/http"
)

// SharedHTTPClient creates an HTTP client with connection sharing across workers based on available ephemeral ports
func SharedHTTPClient() *http.Client {
	totalPorts := PortCount
	maxConns := int(float64(totalPorts) * PortAllocationRatio)

	return &http.Client{
		Timeout: RequestTimeout,
		Transport: &http.Transport{
			MaxConnsPerHost:     maxConns,
			MaxIdleConnsPerHost: maxConns,
			MaxIdleConns:        0,                  // unlimited, MaxIdleConnsPerHost limits per-host idle connections
			IdleConnTimeout:     RequestTimeout * 3, // keep connections warm for a few request cycles
		},
	}
}
