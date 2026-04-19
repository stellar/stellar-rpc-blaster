package config

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/pelletier/go-toml"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/stellar-rpc-blaster/internal/run/parameters"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
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
	StepInterval   time.Duration
	Serial         bool   // run endpoints one at a time instead of concurrently
	TestOutputPath string // path to write JSON results
	ErrorPercent   int    // threshold for error percentage to kill the test (e.g. 50 means 50%)

	// Generate mode settings
	OutputPath   string
	LedgerWindow []uint32
	Count        uint32

	InputDataPath string `toml:"input_data_path"` // path to read seed data for data-dependent endpoints, output by generate mode
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
	InputDataPath  string
	Duration       time.Duration
	RampUp         time.Duration
	StepInterval   time.Duration
	Serial         bool
	ErrorPercent   int

	// Generate mode settings
	OutputPath   string
	LedgerWindow []uint32
	Count        uint32

	Mode Mode
	Ctx  context.Context
}

// Per-endpoint configuration
type EndpointConfig struct {
	RPS      int    `toml:"rps"`                 // requests per second
	StartRPS *int   `toml:"start_rps,omitempty"` // initial RPS to start at, must be <= RPS; nil means omitted
	Limit    uint32 `toml:"limit,omitempty"`     // number of results per request
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
		return Config{}, fmt.Errorf("failed to fetch network passphrase: %w", err)
	} else {
		cfg.NetworkPassphrase = getNetworkResponse.Passphrase
	}

	cfg.ConfigPath = settings.ConfigPath
	cfg.RpcUrl = settings.RpcUrl
	cfg.Mode = settings.Mode
	logger.Debugf("Requested %s mode", settings.Mode.Name())
	switch cfg.Mode {
	case Run:
		cfg.Duration = settings.Duration
		cfg.RampUp = settings.RampUp
		cfg.StepInterval = settings.StepInterval
		cfg.Serial = settings.Serial
		cfg.TestOutputPath = settings.TestOutputPath
		if err := cfg.processToml(settings.ConfigPath); err != nil {
			return Config{}, err
		}
		logger.Infof("Successfully loaded config from %s", settings.ConfigPath)
		if settings.InputDataPath != "" && cfg.InputDataPath != "" {
			return Config{}, fmt.Errorf("input-data-path provided in both CLI and config file; please provide in only one place")
		} else if settings.InputDataPath != "" {
			cfg.InputDataPath = settings.InputDataPath
		}
		if err := cfg.validateEndpointConfig(); err != nil {
			return Config{}, err
		}
		logger.Infof("Successfully loaded seed data from %s", cfg.InputDataPath)
		cfg.ErrorPercent = settings.ErrorPercent
	case Generate:
		cfg.OutputPath = settings.OutputPath
		cfg.LedgerWindow = settings.LedgerWindow
		cfg.Count = settings.Count
	default:
		return Config{}, fmt.Errorf("unknown mode: %s", cfg.Mode.Name())
	}

	return cfg, nil
}

func (c *Config) processToml(tomlPath string) error {
	// Load config TOML file
	cfg, err := toml.LoadFile(tomlPath)
	if err != nil {
		return fmt.Errorf("config file \"%s\" was not found: %w", tomlPath, err)
	}

	// Unmarshal TOML data into the Config struct
	if err = cfg.Unmarshal(c); err != nil {
		return fmt.Errorf("error unmarshalling TOML config: %w", err)
	}

	return nil
}

// Ensure at least one endpoint is configured if launching a load test and data-dependent endpoints have input data
func (c *Config) validateEndpointConfig() error {
	if c.ErrorPercent < 0 || c.ErrorPercent > 100 {
		return fmt.Errorf("error-percent must be between 0 and 100")
	}

	if c.RampUp > 0 {
		if c.StepInterval < 0 || c.StepInterval%(5*time.Second) != 0 {
			return fmt.Errorf("step-interval must be a positive multiple of 5s")
		}
		if c.StepInterval > c.RampUp {
			return fmt.Errorf("step-interval cannot be greater than ramp-up duration")
		}
	}
	hasValidEndpoint := false
	hasStartRPS := false
	for _, endpoint := range c.GetActiveEndpoints() {
		hasValidEndpoint = true
		if needs, err := parameters.EndpointNeedsData(endpoint); err != nil {
			return fmt.Errorf("failed to check if endpoint %s needs data: %w", endpoint, err)
		} else if needs && c.InputDataPath == "" {
			return fmt.Errorf("endpoint %s requires input data, but no input-data-path was provided", endpoint)
		}

		startRPS := c.GetEndpointStartRPS(endpoint)
		if startRPS >= 0 {
			hasStartRPS = true
			if c.GetEndpointTargetRPS(endpoint) < startRPS {
				return fmt.Errorf("could not parse endpoint %s, need start_rps <= rps", endpoint)
			}
		}
		switch endpoint {
		case "getTransactions":
			if c.GetEndpointLimit(endpoint) > util.MaxTxPageLimit {
				return fmt.Errorf("endpoint %s requires a pagination limit under %d", endpoint, util.MaxTxPageLimit)
			}
		case "getLedgers":
			if c.GetEndpointLimit(endpoint) > util.MaxLedgersPageLimit {
				return fmt.Errorf("endpoint %s requires a pagination limit under %d", endpoint, util.MaxLedgersPageLimit)
			}
		case "getEvents":
			if c.GetEndpointLimit(endpoint) > util.MaxEventsPageLimit {
				return fmt.Errorf("endpoint %s requires a pagination limit under %d", endpoint, util.MaxEventsPageLimit)
			}
		default:
			// for endpoints that don't support pagination, limit must be 0
			if c.GetEndpointLimit(endpoint) != 0 {
				return fmt.Errorf("endpoint %s does not support pagination, so limit must be omitted or 0", endpoint)
			}
		}
	}
	if !hasValidEndpoint {
		return fmt.Errorf("at least one endpoint must be configured with RPS > 0")
	}
	if hasStartRPS && c.RampUp == 0 {
		return fmt.Errorf("at least one endpoint is configured with start_rps, but ramp-up duration is not set")
	}
	return nil
}

func (c *Config) GetEndpoints() []string {
	return slices.Collect(maps.Keys(c.Endpoints))
}

func (c *Config) GetEndpointTargetRPS(key string) int {
	if ep, ok := c.Endpoints[key]; ok {
		return ep.RPS
	}
	return 0
}

// GetEndpointStartRPS returns the configured start RPS, or -1 if omitted.
func (c *Config) GetEndpointStartRPS(key string) int {
	if ep, ok := c.Endpoints[key]; ok && ep.StartRPS != nil {
		return *ep.StartRPS
	}
	return -1
}

// GetEndpointLimit returns the configured limit, or 0 if omitted.
func (c *Config) GetEndpointLimit(key string) uint32 {
	if ep, ok := c.Endpoints[key]; ok && ep.Limit != 0 {
		return ep.Limit // if limits are set in toml as non-zero, use them
	}
	switch key {
	case "getLedgers", "getTransactions":
		return util.DefaultTxPageLimit // default limit for ledger-range endpoints
	case "getEvents":
		return util.DefaultEventsPageLimit // default limit for getEvents
	default:
		return 0 // for endpoints that don't support limits
	}
}

// GetActiveEndpoints returns the endpoints configured with RPS > 0.
func (c *Config) GetActiveEndpoints() []string {
	activeEndpoints := make([]string, 0, len(c.Endpoints))
	for _, endpointKey := range c.GetEndpoints() {
		if c.GetEndpointTargetRPS(endpointKey) > 0 {
			activeEndpoints = append(activeEndpoints, endpointKey)
		}
	}
	slices.Sort(activeEndpoints)
	return activeEndpoints
}
