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

	"github.com/sirupsen/logrus"
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
	logger  *log.Entry
	config  config.Config
	client  *http.Client
	logFile *os.File
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
		if err := a.runLoadTest(ctx); err != nil {
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
	start := time.Now()
	filename := fmt.Sprintf("%s-%s.log", runtimeSettings.Mode.Name(), start.Format("2006-01-02T15-04-05"))
	if a.logFile, err = a.SaveLogsToFile(filename); err != nil {
		return fmt.Errorf("could not save logs to file: %w", err)
	}
	a.logger.Info("Starting Blaster")

	a.client = util.SharedHTTPClient()
	if a.config, err = config.NewConfig(ctx, runtimeSettings, a.logger, a.client); err != nil {
		return fmt.Errorf("Could not load configuration: %w", err)
	}
	return nil
}

func (a *App) close() {
	a.logger.Info("Shutting down Blaster")
	if a.logFile != nil {
		a.logFile.Close()
	}
}

func (a *App) runLoadTest(ctx context.Context) error {
	out := make(chan blasterMetrics.Sample, 1000)

	aggregator := blasterMetrics.NewAggregator(a.logger, a.config)

	be, err := engine.NewBlastEngine(ctx, a.logger, a.config, a.client, out)
	if err != nil {
		return fmt.Errorf("could not create blast engine: %w", err)
	}
	// Aggregator goroutine: consumes samples and prints every 5s
	var wg sync.WaitGroup
	wg.Go(func() {
		aggregator.Run(ctx, out)
	})

	be.Run(ctx, a.logger) // run blasts and block until done
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

func (a *App) SaveLogsToFile(filename string) (*os.File, error) {
	dir := filepath.Join("output", "run_logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("could not create log directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, filename), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("could not open log file: %w", err)
	}

	// Keep stdout as the logger output (preserves TTY color detection).
	// Mirror all log entries to the file via a logrus hook.
	a.logger.AddHook(&fileWriterHook{file: f, formatter: &logrus.TextFormatter{
		DisableColors:   true,
		DisableQuote:    true,
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
	}})
	return f, nil
}

// fileWriterHook is a logrus hook that writes log entries to a file with colors stripped.
// This lets the primary logger output to stdout with TTY colors while mirroring to a file.
type fileWriterHook struct {
	file      *os.File
	formatter logrus.Formatter
}

func (h *fileWriterHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *fileWriterHook) Fire(entry *logrus.Entry) error {
	b, err := h.formatter.Format(entry)
	if err != nil {
		return err
	}
	_, err = h.file.Write(b)
	return err
}
