package blaster

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/run/engine"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/run/metrics"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

const (
	nameSpace = "stellar-rpc-blaster"
)

var logger = log.New().WithField("service", nameSpace)

type App struct {
	logger *log.Entry
	config config.Config
	client *http.Client
}

func NewApp() *App {
	logger.SetLevel(log.DebugLevel)
	app := &App{logger: logger}
	return app
}

func (a *App) RunApp(runtimeSettings config.RuntimeSettings) error {
	// Handle OS signals and ctx cancellation to terminate the service
	ctx, cancel := signal.NotifyContext(runtimeSettings.Ctx, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := a.init(ctx, runtimeSettings); err != nil {
		return err
	}

	defer a.close()

	var missingFieldsBuilder strings.Builder

	switch runtimeSettings.Mode {
	case config.Run:
		if runtimeSettings.ConfigPath == "" {
			missingFieldsBuilder.WriteString("config-path, ")
		}
		if runtimeSettings.RpcUrl == "" {
			missingFieldsBuilder.WriteString("rpc-url, ")
		}
		if runtimeSettings.Duration <= 0 {
			missingFieldsBuilder.WriteString("duration, ")
		}
		missingFields := strings.TrimSuffix(missingFieldsBuilder.String(), ", ")

		if missingFields != "" {
			return fmt.Errorf("missing required fields in Run mode: %s", missingFields)
		}
		if err := a.runLoadTest(ctx, cancel); err != nil {
			return err
		}
	case config.Generate:
		if runtimeSettings.OutputPath == "" {
			missingFieldsBuilder.WriteString("output, ")
		}
		missingFields := strings.TrimSuffix(missingFieldsBuilder.String(), ", ")

		if missingFields != "" {
			return fmt.Errorf("missing required fields in Generate mode: %s", missingFields)
		}
		if err := a.runGenerate(ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown mode: %s", runtimeSettings.Mode.Name())
	}

	a.logger.Infof("Blaster finished successfully")
	return nil
}

func (a *App) init(ctx context.Context, runtimeSettings config.RuntimeSettings) error {
	var err error
	a.logger.Info("Starting Blaster")

	// Set up output directory + saved log files alongside console output
	if err := a.setOutput(&runtimeSettings); err != nil {
		return err
	}

	// Initialize client and load config
	a.client = util.SharedHTTPClient()
	if a.config, err = config.NewConfig(ctx, runtimeSettings, a.logger, a.client); err != nil {
		return fmt.Errorf("Could not load configuration: %w", err)
	}
	return nil
}

func (a *App) close() {
	a.logger.Info("Shutting down Blaster")
}

func (a *App) runLoadTest(ctx context.Context, cancel context.CancelFunc) error {
	out := make(chan blasterMetrics.Sample, 1000) // engine writes samples to this channel, aggregator reads from it

	be, err := engine.NewBlastEngine(ctx, a.logger, a.config, a.client, out)
	if err != nil {
		return fmt.Errorf("could not create blast engine: %w", err)
	}

	aggregator := blasterMetrics.NewAggregator(a.logger, a.config, cancel)
	// Aggregator goroutine: consumes samples and prints every 5s
	var wg sync.WaitGroup
	wg.Go(func() {
		aggregator.Run(ctx, out)
	})

	be.Run(ctx, a.logger, aggregator) // run blasts and block until done
	close(out)
	wg.Wait()

	return err
}

func (a *App) runGenerate(ctx context.Context) error {
	g, err := generate.NewGenerator(ctx, a.config)
	if err != nil {
		return fmt.Errorf("could not make new generator: %w", err)
	}
	return g.Generate(ctx, a.logger, a.config)
}

func (a *App) setOutput(runtimeSettings *config.RuntimeSettings) error {
	if runtimeSettings.Mode == config.Run {
		if err := os.MkdirAll(runtimeSettings.TestOutputPath, 0o755); err != nil {
			return fmt.Errorf("could not create output directory: %w", err)
		}
		filename := fmt.Sprintf("test-results-%s.json", time.Now().Format("2006-01-02T15-04-05"))
		runtimeSettings.TestOutputPath = filepath.Join(runtimeSettings.TestOutputPath, filename)
	}
	return nil
}
