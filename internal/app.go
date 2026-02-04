package blaster

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/engine"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/metrics"
)

const (
	nameSpace = "stellar-rpc-blaster"
)

var logger = log.New().WithField("service", nameSpace)

type App struct {
	logger     *log.Entry
	config     *Config
	aggregator *blasterMetrics.Aggregator
}

func NewApp() *App {
	logger.SetLevel(log.DebugLevel)
	app := &App{logger: logger}
	return app
}

func (a *App) Run(runtimeSettings RuntimeSettings) error {
	// Handle OS signals and ctx cancellation to terminate the service
	ctx, cancel := signal.NotifyContext(runtimeSettings.Ctx, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := a.init(runtimeSettings); err != nil {
		return err
	}

	defer a.close()

	switch runtimeSettings.Mode {
	case Run:
		if err := a.runLoadTest(ctx); err != nil {
			return err
		}
	case Generate:
		return errors.New("Generate mode not implemented yet")
	default:
		return errors.Errorf("unknown mode: %v", runtimeSettings.Mode)
	}

	a.logger.Infof("Blaster finished successfully")
	return nil
}

func (a *App) init(runtimeSettings RuntimeSettings) error {
	var err error

	a.logger.Info("Starting Blaster")

	if a.config, err = NewConfig(runtimeSettings); err != nil {
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

	err := engine.RunVegeta(ctx, a.config, out)
	close(out)
	wg.Wait()

	return err
}
