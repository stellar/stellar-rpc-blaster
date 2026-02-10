package generate

import (
	"context"
	"time"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/config"
	"github.com/stellar/stellar-rpc-blaster/internal/generate/seed"
)

type Generator struct {
	rpcUrl     string
	client     *rpcclient.Client
	parameters seed.PreseedParameters

	start time.Time
	end   time.Time

	exportPath string
	methods    []string
}

func NewGenerator(ctx context.Context, config config.Config) *Generator {
	first, last, err := GetLedgerRange(ctx, config.RpcClient, config.LedgerWindow)
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
	return &Generator{
		rpcUrl:     config.RpcUrl,
		client:     config.RpcClient,
		parameters: parameters,
		methods:    []string{"getLedger", "getTransaction", "getAccount", "getEffects"}, // TODO: set elsewhere
	}
}

func (g *Generator) Generate(ctx context.Context, logger *log.Entry, cfg config.Config) error {
	logger.Infof("Starting data generation for ledger range [%d, %d]", g.parameters.Range.First, g.parameters.Range.Last)
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
	if err != nil {
		return errors.Wrap(err, "failed to create seed writer")
	}
	defer writer.Close()

	// Write the relevant ledger range as the first entry
	if err := seed.WriteLedgerRangeEntry(g.parameters, writer); err != nil {
		return errors.Wrap(err, "failed to write ledger range entry")
	}
	// Bootstrap transaction hashes and success status within the ledger range
	if err := seed.SeedTxHashData(ctx, g.client, writer, g.parameters); err != nil {
		return errors.Wrap(err, "failed to seed transaction hash data")
	}
	// Bootstrap active contract IDs within the ledger range
	if err := seed.SeedContractIdData(ctx, g.client, writer, g.parameters); err != nil {
		return errors.Wrap(err, "failed to seed contract id data")
	}

	// TODO: add more seed functions here, e.g.:
	// if err := seed.SeedLedgerData(ctx, g.client, writer, g.parameters); err != nil { ... }

	logger.Infof("Generated data written to %s", cfg.OutputPath)
	return nil
}
