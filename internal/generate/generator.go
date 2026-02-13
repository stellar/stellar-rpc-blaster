package generate

import (
	"context"
	"time"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/writer"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type Generator struct {
	rpcUrl     string
	client     *rpcclient.Client
	parameters util.PreseedParameters
}

func NewGenerator(ctx context.Context, config config.Config) *Generator {
	first, last, err := util.GetLedgerRange(ctx, config.RpcClient, config.LedgerWindow, config.Count)
	if err != nil {
		panic(errors.Wrap(err, "failed to get ledger range for generation"))
	}

	parameters := util.PreseedParameters{
		ExportPath: config.OutputPath,
		Range: util.Range{
			First: first,
			Last:  last,
		},
	}

	// If the window contains more ledgers than count, sample uniformly
	if sampledLedgers := util.ComputeSampledLedgers(first, last, config.Count); sampledLedgers != nil {
		parameters.ProcessingRanges = util.GroupSampledLedgersIntoRanges(sampledLedgers)
	}

	return &Generator{
		rpcUrl:     config.RpcUrl,
		client:     config.RpcClient,
		parameters: parameters,
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

	writer, err := writer.NewSeedWriter(cfg.OutputPath)
	if err != nil {
		return errors.Wrap(err, "failed to create seed writer")
	}

	// Set the ledger range
	writer.Output.LedgerRange = g.parameters.Range
	logger.Infof("Successfully wrote ledger range [%d, %d] to output",
		g.parameters.Range.First, g.parameters.Range.Last)

	// Bootstrap transaction hashes and success status within the ledger range
	txHashes, err := seed.SeedTxHashData(ctx, g.client, g.parameters)
	if err != nil {
		return errors.Wrap(err, "failed to seed transaction hash data")
	}
	writer.Output.TxHashes = txHashes
	logger.Infof("Successfully wrote %d {transaction hash : success status} entries to output", len(txHashes))

	// Bootstrap active contract IDs and event topics within the ledger range
	writer.Output.ContractIDs, writer.Output.EventTopics, err = seed.SeedEventsData(ctx, g.client, g.parameters)
	if err != nil {
		return errors.Wrap(err, "failed to seed events data")
	}
	logger.Infof("Successfully wrote %d active contract IDs entries to output", len(writer.Output.ContractIDs))
	logger.Infof("Successfully wrote %d unique event topics entries to output", len(writer.Output.EventTopics))

	// Seed ledger keys
	writer.Output.LedgerKeys, err = seed.SeedLedgerKeys(ctx, logger, g.client, g.parameters)
	if err != nil {
		return errors.Wrap(err, "failed to seed ledger keys")
	}
	logger.Infof("Successfully wrote %d ledger keys entries to output", len(writer.Output.LedgerKeys))

	if err := writer.Flush(); err != nil {
		return errors.Wrap(err, "failed to flush seed data to output")
	}

	logger.Infof("Generated data written to %s", cfg.OutputPath)
	return nil
}
