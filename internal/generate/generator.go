package generate

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type Generator struct {
	Seeders    []seed.Seeder
	Writer     *seed.SeedWriter
	parameters seed.PreseedParameters
}

func NewGenerator(ctx context.Context, cfg config.Config) (*Generator, error) {
	window, err := seed.GetLedgerRange(ctx, cfg.RpcClient, cfg.LedgerWindow, cfg.Count)
	if err != nil {
		return nil, fmt.Errorf("failed to get ledger range for generation: %w", err)
	}

	parameters := seed.PreseedParameters{
		ExportPath: cfg.OutputPath,
		Range:      window,
	}

	// If the window contains more ledgers than count, sample uniformly
	if cfg.Count < window.Last-window.First+1 {
		sampledLedgers, err := seed.ComputeSampledLedgers(window, cfg.Count)
		if err != nil {
			return nil, fmt.Errorf("could not get sample of ledgers: %w", err)
		}
		parameters.ProcessingRanges = seed.GroupSampledLedgersIntoRanges(sampledLedgers)
	}

	writer, err := seed.NewSeedWriter(cfg.OutputPath, parameters.Range)
	if err != nil {
		return nil, fmt.Errorf("failed to create seed writer: %w", err)
	}

	return &Generator{
		parameters: parameters,
		Writer:     writer,
		Seeders: []seed.Seeder{
			seed.NewTxHashSeeder(cfg.RpcClient),
			seed.NewEventDataSeeder(cfg.RpcClient),
			seed.NewLedgerKeySeeder(cfg.RpcClient),
		},
	}, nil
}

func (g *Generator) Generate(ctx context.Context, logger *log.Entry, cfg config.Config) error {
	logger.Infof("Starting data generation for ledger range [%d, %d]", g.parameters.Range.First, g.parameters.Range.Last)
	if len(g.parameters.ProcessingRanges) > 0 {
		logger.Infof("Sampling %d sub-ranges (%d sampled ledgers) from window of %d ledgers",
			len(g.parameters.ProcessingRanges), cfg.Count,
			g.parameters.Range.Last-g.parameters.Range.First+1)
	}
	start := time.Now()

	for _, s := range g.Seeders {
		if err := seed.RunSeeder(ctx, g.parameters, s); err != nil {
			return fmt.Errorf("failed to run seeder for %s: %w", s, err)
		}
		s.WriteResults(g.Writer)
		logger.Infof("Successfully fetched %s %s", s, util.LogElapsed(start))
	}

	if err := g.Writer.Close(); err != nil {
		return fmt.Errorf("failed to close seed writer: %w", err)
	}

	logger.Infof("Generated data written to %s %s", cfg.OutputPath, util.LogElapsed(start))
	return nil
}
