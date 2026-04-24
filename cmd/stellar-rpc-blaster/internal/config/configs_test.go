package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateEndpointConfigIgnoresInactiveEndpoints(t *testing.T) {
	cfg := Config{
		Endpoints: map[string]EndpointConfig{
			"notARealEndpoint": {RPS: 0},
			"getHealth":       {RPS: 1},
		},
	}

	require.NoError(t, cfg.validateEndpointConfig())
}

func TestValidateEndpointConfigRequiresActiveEndpoint(t *testing.T) {
	cfg := Config{
		Endpoints: map[string]EndpointConfig{
			"getHealth": {RPS: 0},
			"getEvents": {RPS: 0},
		},
	}

	err := cfg.validateEndpointConfig()
	require.Error(t, err)
	require.ErrorContains(t, err, "at least one endpoint must be configured with RPS > 0")
}