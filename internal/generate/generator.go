package generate

import (
	"context"
	"time"

	"github.com/pkg/errors"

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

	meta GeneratorMeta
}

type GeneratorMeta struct {
	txHashCount           uint32
	contractIdCount       uint32
	uniqueEventTopicCount uint32
	ledgerKeyCount        uint32
}

func NewGenerator(ctx context.Context, config config.Config) *Generator {
	first, last, err := util.GetLedgerRange(ctx, config.RpcClient, config.LedgerWindow, config.Count)
	if err != nil {
		panic(errors.Wrap(err, "failed to get ledger range for generation"))
	}

	parameters := seed.PreseedParameters{
		ExportPath: config.OutputPath,
		Range: seed.Range{
			First: first,
			Last:  last,
		},
	}

	// If the window contains more ledgers than count, sample uniformly
	if sampledLedgers := util.ComputeSampledLedgers(first, last, config.Count); sampledLedgers != nil {
		parameters.ProcessingRanges = seed.GroupSampledLedgersIntoRanges(sampledLedgers)
	}

	return &Generator{
		rpcUrl:     config.RpcUrl,
		client:     config.RpcClient,
		parameters: parameters,
		meta:       GeneratorMeta{},
	}
}

func (g *Generator) Generate(ctx context.Context, logger *log.Entry, cfg config.Config) error {
	logger.Infof("Starting data generation for ledger range [%d, %d]", g.parameters.Range.First, g.parameters.Range.Last)
	if len(g.parameters.ProcessingRanges) > 0 {
		logger.Infof("Sampling %d sub-ranges (%d sampled ledgers) from window of %d ledgers",
			len(g.parameters.ProcessingRanges), cfg.Count,
			g.parameters.Range.Last-g.parameters.Range.First+1)
	}
	start := time.Now()
	defer func() {
		logger.Infof("Data generation completed in %s", time.Since(start))
	}()
	getNetworkResponse, err := cfg.RpcClient.GetNetwork(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to fetch network passphrase during generation")
	}
	passphrase := getNetworkResponse.Passphrase
	logger.Infof("Fetched network passphrase: %s", passphrase)

	writer, err := seed.NewSeedWriter(cfg.OutputPath)

	// Write the relevant ledger range as the first entry
	if err := seed.WriteLedgerRangeEntry(g.parameters, writer); err != nil {
		return errors.Wrap(err, "failed to write ledger range entry")
	}
	logger.Infof("Successfully wrote ledger range [%d, %d] to output",
		g.parameters.Range.First, g.parameters.Range.Last)

	// Bootstrap transaction hashes and success status within the ledger range
	if g.meta.txHashCount, err = seed.SeedTxHashData(ctx, g.client, writer, g.parameters); err != nil {
		return errors.Wrap(err, "failed to seed transaction hash data")
	}
	logger.Infof("Successfully wrote %d {transaction hash : success status} entries to output", g.meta.txHashCount)

	// Bootstrap active contract IDs within the ledger range
	if g.meta.contractIdCount, g.meta.uniqueEventTopicCount, err = seed.SeedEventsData(ctx, g.client, writer, g.parameters); err != nil {
		return errors.Wrap(err, "failed to seed events data")
	}
	logger.Infof("Successfully wrote %d active contract IDs entries to output", g.meta.contractIdCount)
	logger.Infof("Successfully wrote %d unique event topics entries to output", g.meta.uniqueEventTopicCount)

	// Seed ledger keys
	if g.meta.ledgerKeyCount, err = seed.SeedLedgerKeys(ctx, logger, g.client, writer, g.parameters); err != nil {
		return errors.Wrap(err, "failed to seed ledger keys")
	}
	logger.Infof("Successfully wrote %d ledger keys entries to output", g.meta.ledgerKeyCount)

	if err := writer.Flush(); err != nil {
		return errors.Wrap(err, "failed to flush seed data to output")
	}

	logger.Infof("Generated data written to %s", cfg.OutputPath)
	return nil
}
