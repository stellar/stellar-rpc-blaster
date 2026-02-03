package blaster

import (
	"context"
	"fmt"
	"time"

	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
)

const (
	GetHealthField       = "getHealth"
	GetNetworkField      = "getNetwork"
	GetVersionInfoField  = "getVersionInfo"
	GetLatestLedgerField = "getLatestLedger"
)

type Mode int

const (
	_   Mode = iota
	Run Mode = iota
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
	RPCUrl            string

	// Run mode settings
	ConfigPath        string
	ExportMetricsPath string
	Duration          time.Duration
	RampUp            time.Duration

	// Generate mode settings
	SeedPath     string
	LedgerWindow uint32

	Mode Mode
	Ctx  context.Context
}

// Per-endpoint configuration
type EndpointConfig struct {
	RPS        int    `toml:"rps"`                 // requests per second
	NumClients int    `toml:"num_clients"`         // number of concurrent clients performing rps
	DataPath   string `toml:"data_path,omitempty"` // path to data file for data-dependent endpoints
}

type Config struct {
	Endpoints map[string]EndpointConfig `toml:"endpoints"`

	// TODO: data-dependent endpoints

	ConfigPath        string
	NetworkPassphrase string
	RPCUrl            string
	Duration          time.Duration
	RampUp            time.Duration
	Mode              Mode
}

func NewConfig(settings RuntimeSettings) (*Config, error) {
	config := &Config{}

	config.ConfigPath = settings.ConfigPath
	config.NetworkPassphrase = settings.NetworkPassphrase
	config.RPCUrl = settings.RPCUrl
	config.Duration = settings.Duration
	config.RampUp = settings.RampUp
	config.Mode = settings.Mode

	logger.Infof("Requested %v mode", settings.Mode.Name())

	var err error
	if err = config.processToml(settings.ConfigPath); err != nil {
		return nil, err
	}
	logger.Infof("Sucessfully loaded config from %s", settings.ConfigPath)

	return config, nil
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
	for _, endpointData := range c.Endpoints {
		if endpointData.RPS > 0 && endpointData.NumClients > 0 {
			return nil
		}
	}
	return fmt.Errorf("at least one endpoint must be configured with RPS > 0 and NumClients > 0")
}

// Implement engine.RunEngine interface
func (c *Config) GetRpcUrl() string {
	return c.RPCUrl
}

func (c *Config) GetDuration() time.Duration {
	return c.Duration
}

func (c *Config) GetRampUp() time.Duration {
	return c.RampUp
}

func (c *Config) GetEndpoints() []string {
	result := make([]string, 0, len(c.Endpoints))
	for k := range c.Endpoints {
		result = append(result, k)
	}
	return result
}

func (c *Config) GetEndpoint(key string) (rps int, numClients int) {
	if ep, ok := c.Endpoints[key]; ok {
		return ep.RPS, ep.NumClients
	}
	return 0, 0
}
