package config

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters"
)

type Config struct {
	Endpoints map[string]EndpointConfig `toml:"endpoints"`
	RpcClient *rpcclient.Client

	// Common settings
	ConfigPath        string
	NetworkPassphrase string
	RpcUrl            string
	Mode              Mode

	// Run mode settings
	Duration       time.Duration
	RampUp         time.Duration
	TestOutputPath string // path to write JSON results

	// Generate mode settings
	OutputPath   string
	LedgerWindow []uint32
	Count        uint32

	InputDataPath string // path to read seed data for data-dependent endpoints, output by generate mode
}

type Mode int

const (
	_ Mode = iota
	Run
	Generate
)

func (m Mode) Name() string {
	switch m {
	case Run:
		return "Run"
	case Generate:
		return "Generate"
	}
	return "none"
}

type RuntimeSettings struct {
	// Common settings
	NetworkPassphrase string
	RpcUrl            string

	// Run mode settings
	ConfigPath     string
	TestOutputPath string
	Duration       time.Duration
	RampUp         time.Duration

	// Generate mode settings
	OutputPath   string
	LedgerWindow []uint32
	Count        uint32

	Mode Mode
	Ctx  context.Context
}

// Per-endpoint configuration
type EndpointConfig struct {
	RPS int `toml:"rps"` // requests per second
}

func NewConfig(
	ctx context.Context,
	settings RuntimeSettings,
	logger *log.Entry,
	client *http.Client,
) (Config, error) {
	cfg := Config{}
	cfg.RpcClient = rpcclient.NewClient(settings.RpcUrl, client)

	if getNetworkResponse, err := cfg.RpcClient.GetNetwork(ctx); err != nil {
		return Config{}, errors.Wrap(err, "failed to fetch network passphrase")
	} else {
		cfg.NetworkPassphrase = getNetworkResponse.Passphrase
	}

	cfg.ConfigPath = settings.ConfigPath
	cfg.RpcUrl = settings.RpcUrl
	cfg.Mode = settings.Mode
	logger.Debugf("Requested %v mode", settings.Mode.Name())
	switch cfg.Mode {
	case Run:
		cfg.Duration = settings.Duration
		cfg.RampUp = settings.RampUp
		cfg.TestOutputPath = settings.TestOutputPath
		if err := cfg.processToml(settings.ConfigPath); err != nil {
			return Config{}, err
		}
	case Generate:
		cfg.OutputPath = settings.OutputPath
		cfg.LedgerWindow = settings.LedgerWindow
		cfg.Count = settings.Count
	default:
		return Config{}, errors.Errorf("unknown mode: %v", cfg.Mode)
	}

	logger.Infof("Successfully loaded config from %s", settings.ConfigPath)

	return cfg, nil
}

func (c *Config) processToml(tomlPath string) error {
	// Load config TOML file
	cfg, err := toml.LoadFile(tomlPath)
	if err != nil {
		return errors.Wrapf(err, "config file %v was not found", tomlPath)
	}

	// Unmarshal TOML data into the Config struct
	if err = cfg.Unmarshal(c); err != nil {
		return errors.Wrap(err, "Error unmarshalling TOML config.")
	}

	if c.Mode == Run {
		if err = c.validateEndpointConfig(); err != nil {
			return err
		}
	}

	return nil
}

// Ensure at least one endpoint is configured if launching a load test and data-dependent endpoints have input data
func (c *Config) validateEndpointConfig() error {
	hasValidEndpoint := false
	for endpoint, endpointData := range c.Endpoints {
		if endpointData.RPS > 0 {
			hasValidEndpoint = true
		}
		if parameters.EndpointNeedsData(endpoint) && c.InputDataPath == "" {
			return errors.Errorf("endpoint %s requires input data, but no input-data-path was provided", endpoint)
		}
	}
	if !hasValidEndpoint {
		return errors.Errorf("at least one endpoint must be configured with RPS > 0")
	}
	return nil
}

func (c *Config) GetEndpoints() []string {
	return slices.Collect(maps.Keys(c.Endpoints))
}

func (c *Config) GetEndpointRPS(key string) int {
	if ep, ok := c.Endpoints[key]; ok {
		return ep.RPS
	}
	return 0
}
