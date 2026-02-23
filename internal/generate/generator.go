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
	logger.Debugf("Successfully wrote ledger sequence numbers start: %d, end: %d to output %s",
		g.parameters.Range.First, g.parameters.Range.Last, util.LogElapsed(start))

	// Bootstrap transaction hashes and success status within the ledger range
	txHashSeeder := seed.NewTxHashSeeder()
	if err := seed.RunSeeder(ctx, logger, g.client, g.parameters, txHashSeeder); err != nil {
		return errors.Wrap(err, "failed to seed transaction hash data")
	}
	writer.Output.TxHashes = txHashSeeder.Results()
	logger.Infof("Successfully wrote %d successful and %d failed transaction hashes to output %s",
		len(writer.Output.TxHashes.Successes), len(writer.Output.TxHashes.Failures), util.LogElapsed(start))

	// Bootstrap active contract IDs and event topics within the ledger range
	eventSeeder := seed.NewEventDataSeeder()
	if err := seed.RunSeeder(ctx, logger, g.client, g.parameters, eventSeeder); err != nil {
		return errors.Wrap(err, "failed to seed events data")
	}
	writer.Output.ContractIDs = eventSeeder.ContractIDs()
	writer.Output.EventTopics = eventSeeder.EventTopics()
	logger.Infof("Successfully wrote %d active contract IDs entries to output %s",
		len(writer.Output.ContractIDs), util.LogElapsed(start))
	logger.Infof("Successfully wrote %d unique event topics entries to output %s",
		len(writer.Output.EventTopics), util.LogElapsed(start))

	// Seed ledger keys
	ledgerKeySeeder := seed.NewLedgerKeySeeder()
	if err := seed.RunSeeder(ctx, logger, g.client, g.parameters, ledgerKeySeeder); err != nil {
		return errors.Wrap(err, "failed to seed ledger keys")
	}
	writer.Output.LedgerKeys = ledgerKeySeeder.Results()
	logger.Infof("Successfully wrote %d ledger keys entries to output %s",
		len(writer.Output.LedgerKeys), util.LogElapsed(start))

	if err := writer.Flush(); err != nil {
		return errors.Wrap(err, "failed to flush seed data to output")
	}

	logger.Infof("Generated data written to %s %s", cfg.OutputPath, util.LogElapsed(start))
	return nil
}
