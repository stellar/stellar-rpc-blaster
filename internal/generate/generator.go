package generate

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type Generator struct {
	rpcUrl     string
	client     *rpcclient.Client
	parameters seed.PreseedParameters
}

func NewGenerator(ctx context.Context, config config.Config) (*Generator, error) {
	window, err := seed.GetLedgerRange(ctx, config.RpcClient, config.LedgerWindow, config.Count)
	if err != nil {
		return nil, fmt.Errorf("failed to get ledger range for generation: %v", err)
	}

	parameters := seed.PreseedParameters{
		ExportPath: config.OutputPath,
		Range:      window,
	}

	// If the window contains more ledgers than count, sample uniformly
	if config.Count < window.Last-window.First+1 {
		sampledLedgers, err := seed.ComputeSampledLedgers(window, config.Count)
		if err != nil {
			return nil, fmt.Errorf("could not get sample of ledgers: %v", err)
		}
		parameters.ProcessingRanges = seed.GroupSampledLedgersIntoRanges(sampledLedgers)
	}

	return &Generator{
		rpcUrl:     config.RpcUrl,
		client:     config.RpcClient,
		parameters: parameters,
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

	writer, err := seed.NewSeedWriter(cfg.OutputPath)
	if err != nil {
		return fmt.Errorf("failed to create seed writer: %v", err)
	}

	// Set the ledger range
	writer.LedgerRange = g.parameters.Range

	for _, s := range []seed.Seeder{
		seed.NewTxHashSeeder(g.client, logger),
		seed.NewEventDataSeeder(g.client, logger),
		seed.NewLedgerKeySeeder(g.client, logger),
	} {
		if err := seed.RunSeeder(ctx, g.parameters, s); err != nil {
			return fmt.Errorf("failed to run seeder for %s: %v", s, err)
		}
		s.WriteResults(writer)
		logger.Infof("Successfully fetched %s %s", s, util.LogElapsed(start))
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close seed writer: %v", err)
	}

	logger.Infof("Generated data written to %s %s", cfg.OutputPath, util.LogElapsed(start))
	return nil
}
