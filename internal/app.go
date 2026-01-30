package blaster

import (
	"context"
	"fmt"
	"os"
	"os/signal"
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
	config *Config
}

func NewApp() *App {
	logger.SetLevel(log.DebugLevel)
	app := &App{}
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
		return fmt.Errorf("Generate mode not implemented yet")
	default:
		return fmt.Errorf("unknown mode: %v", runtimeSettings.Mode)
	}

	logger.Infof("Blaster finished successfully")
	return nil
}

func (a *App) init(runtimeSettings RuntimeSettings) error {
	var err error

	logger.Info("Starting Blaster")

	if a.config, err = NewConfig(runtimeSettings); err != nil {
		return errors.Wrap(err, "Could not load configuration")
	}
	return nil
}

func (a *App) close() {
	logger.Info("Shutting down Blaster")
	// TODO: Clean up here if needed (e.g. close DB connection/metrics file)
	// this isn't implemented yet
}

func (a *App) runLoadTest(ctx context.Context) error {
	out := make(chan blasterMetrics.Sample, 1000)
	defer close(out)

	// Collect samples (TODO: export to file + modify to log every 5s)
	go func() {
		for sample := range out {
			logger.Debugf("[%s] latency=%v code=%d ok=%v err=%s",
				sample.Endpoint, sample.Latency, sample.Code, sample.OK, sample.Err)
		}
	}()

	err := engine.RunVegeta(ctx, a.config, out)
	return err
}
