package blaster

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	"github.com/stellar/stellar-rpc-blaster/internal/generate"
	"github.com/stellar/stellar-rpc-blaster/internal/run/engine"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/run/metrics"
)

const (
	nameSpace = "stellar-rpc-blaster"
)

var logger = log.New().WithField("service", nameSpace)

type App struct {
	logger     *log.Entry
	config     config.Config
	aggregator *blasterMetrics.Aggregator
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

	if err := a.init(runtimeSettings); err != nil {
		return err
	}

	defer a.close()

	var missingFields []string

	switch runtimeSettings.Mode {
	case config.Run:
		if runtimeSettings.ConfigPath == "" {
			missingFields = append(missingFields, "config-path")
		}

		if len(missingFields) > 0 {
			return errors.Errorf("missing required fields in Run mode: %v", missingFields)
		}
		if err := a.runLoadTest(ctx); err != nil {
			return err
		}
	case config.Generate:
		if runtimeSettings.SeedPath == "" {
			missingFields = append(missingFields, "seed-path")
		}
		if runtimeSettings.LedgerWindow == 0 {
			missingFields = append(missingFields, "ledger-window")
		}

		if len(missingFields) > 0 {
			return errors.Errorf("missing required fields in Generate mode: %v", missingFields)
		}
		if err := a.runLoadTest(ctx); err != nil {
			return err
		}
	default:
		return errors.Errorf("unknown mode: %v", runtimeSettings.Mode)
	}

	a.logger.Infof("Blaster finished successfully")
	return nil
}

func (a *App) init(runtimeSettings config.RuntimeSettings) error {
	var err error

	a.logger.Info("Starting Blaster")

	if a.config, err = config.NewConfig(runtimeSettings, a.logger); err != nil {
		return errors.Wrap(err, "Could not load configuration")
	}
	return nil
}

func (a *App) close() {
	a.logger.Info("Shutting down Blaster")
	// TODO: Clean up here if needed (e.g. close DB connection/metrics file)
	// this isn't implemented yet
}

func (a *App) runLoadTest(ctx context.Context) error {
	out := make(chan blasterMetrics.Sample, 1000)

	a.aggregator = blasterMetrics.NewAggregator(a.logger, a.config)

	// Aggregator goroutine: consumes samples and prints every 5s
	var wg sync.WaitGroup
	wg.Go(func() {
		a.aggregator.Run(ctx, out)
	})

	err := engine.RunVegeta(ctx, a.logger, a.config, out)
	close(out)
	wg.Wait()

	return err
}

func (a *App) runGenerate(ctx context.Context) error {
	g := generate.NewGenerator(a.config)
	return g.Generate(ctx, a.logger)
}
