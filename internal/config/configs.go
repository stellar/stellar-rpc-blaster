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

	// TODO: data-dependent endpoints & generate mode settings
	SeedPath     string
	LedgerWindow uint32
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
	SeedPath     string
	LedgerWindow uint32

	Mode Mode
	Ctx  context.Context
}

// Per-endpoint configuration
type EndpointConfig struct {
	RPS      int    `toml:"rps"`                 // requests per second
	DataPath string `toml:"data_path,omitempty"` // path to data file for data-dependent endpoints
}

func NewConfig(
	ctx context.Context,
	settings RuntimeSettings,
	logger *log.Entry,
	client *http.Client,
) (Config, error) {
	cfg := Config{}
	cfg.RpcClient = rpcclient.NewClient(settings.RpcUrl, client)

	getNetworkResponse, err := cfg.RpcClient.GetNetwork(ctx)
	if err != nil {
		return Config{}, errors.Wrap(err, "failed to fetch network passphrase")
	} else {
		cfg.NetworkPassphrase = getNetworkResponse.Passphrase
	}

	cfg.ConfigPath = settings.ConfigPath
	cfg.RpcUrl = settings.RpcUrl
	cfg.Mode = settings.Mode
	switch cfg.Mode {
	case Run:
		cfg.Duration = settings.Duration
		cfg.RampUp = settings.RampUp
		cfg.TestOutputPath = settings.TestOutputPath
	case Generate:
		cfg.SeedPath = settings.SeedPath
		cfg.LedgerWindow = settings.LedgerWindow
	default:
		return Config{}, errors.Errorf("unknown mode: %v", cfg.Mode)
	}

	logger.Infof("Requested %v mode", settings.Mode.Name())

	if err := cfg.processToml(settings.ConfigPath); err != nil {
		return Config{}, err
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

	// Ensure at least one endpoint is configured if launching a load test
	if c.Mode == Run {
		if err = c.validateEndpointConfig(); err != nil {
			return err
		}
	}

	return nil
}

func (c *Config) validateEndpointConfig() error {
	hasValidEndpoint := false
	for _, endpointData := range c.Endpoints {
		if endpointData.RPS > 0 {
			hasValidEndpoint = true
		}
	}
	if !hasValidEndpoint {
		return errors.New("at least one endpoint must be configured with RPS > 0")
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
