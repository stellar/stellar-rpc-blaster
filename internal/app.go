package blaster

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	"github.com/stellar/stellar-rpc-blaster/internal/generate"
	"github.com/stellar/stellar-rpc-blaster/internal/run/engine"
	blasterMetrics "github.com/stellar/stellar-rpc-blaster/internal/run/metrics"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

const (
	nameSpace = "stellar-rpc-blaster"
)

var logger = log.New().WithField("service", nameSpace)

type App struct {
	logger *log.Entry
	config config.Config
	client *http.Client

	// Populated if in Run mode
	blastEngine *engine.BlastEngine
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
			return fmt.Errorf("missing required fields in Run mode: %v", missingFields)
		}
		if err := a.runLoadTest(ctx); err != nil {
			return err
		}
	case config.Generate:
		if runtimeSettings.OutputPath == "" {
			missingFieldsBuilder.WriteString("output, ")
		}
		missingFields := strings.TrimSuffix(missingFieldsBuilder.String(), ", ")

		if missingFields != "" {
			return fmt.Errorf("missing required fields in Generate mode: %v", missingFields)
		}
		if err := a.runGenerate(ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown mode: %v", runtimeSettings.Mode)
	}

	a.logger.Infof("Blaster finished successfully")
	return nil
}

func (a *App) init(ctx context.Context, runtimeSettings config.RuntimeSettings) error {
	var err error

	a.logger.Info("Starting Blaster")

	a.client = util.SharedHTTPClient()
	if a.config, err = config.NewConfig(ctx, runtimeSettings, a.logger, a.client); err != nil {
		return fmt.Errorf("Could not load configuration: %v", err)
	}
	return nil
}

func (a *App) close() {
	a.logger.Info("Shutting down Blaster")
	if a.blastEngine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := a.blastEngine.Close(ctx, a.logger); err != nil {
			a.logger.Errorf("failed to close blast engine: %v", err)
		}
	}
}

func (a *App) runLoadTest(ctx context.Context) error {
	out := make(chan blasterMetrics.Sample, 1000)

	aggregator := blasterMetrics.NewAggregator(a.logger, a.config)
	be, err := engine.NewBlastEngine(ctx, a.logger, a.config, a.client, out, aggregator.Start)
	if err != nil {
		return fmt.Errorf("failed to create blast engine: %v", err)
	}
	a.blastEngine = be

	// Aggregator goroutine: consumes samples and prints every 5s
	var wg sync.WaitGroup
	wg.Go(func() {
		aggregator.Run(ctx, out)
	})

	be.FireBlasts(ctx)
	wg.Wait()

	return err
}

func (a *App) runGenerate(ctx context.Context) error {
	g, err := generate.NewGenerator(ctx, a.config)
	if err != nil {
		return fmt.Errorf("could not make new generator: %v", err)
	}
	return g.Generate(ctx, a.logger, a.config)
}
