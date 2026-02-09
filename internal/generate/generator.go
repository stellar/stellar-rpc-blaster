package generate

import (
	"context"
	"os"
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
		MinLedger:  first,
		MaxLedger:  last,
	}
	return &Generator{
		rpcUrl:     config.RpcUrl,
		client:     config.RpcClient,
		parameters: parameters,
		methods:    []string{"getLedger", "getTransaction", "getAccount", "getEffects"}, // TODO: set elsewhere
	}
}

func (g *Generator) Generate(ctx context.Context, logger *log.Entry, cfg config.Config) error {
	logger.Infof("Starting data generation for ledger range [%d, %d]", g.parameters.MinLedger, g.parameters.MaxLedger)
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

	if err := seed.SeedTxHashData(ctx, g.client, g.parameters); err != nil {
		return errors.Wrap(err, "failed to seed transaction hash data")
	}
	fileBytes, _ := os.ReadFile(cfg.OutputPath)
	logger.Infof("Generated data written to %s: %d bytes", cfg.OutputPath, len(fileBytes))
	return errors.Wrap(nil, "data generation not finished yet")
}
