package generate

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	checkpoint "github.com/stellar/go-stellar-sdk/historyarchive"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type PreseedParameters struct {
	MinLedger uint32
	MaxLedger uint32
}

func GetLatestCheckpointLedger(ctx context.Context, rpcClient *rpcclient.Client) (uint32, error) {
	checkpointManager := checkpoint.NewCheckpointManager(util.CheckpointFrequency)
	latestLedger, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "failed to fetch latest ledger")
	}

	return checkpointManager.PrevCheckpoint(latestLedger.Sequence), nil
}

func GetLedgerRange(ctx context.Context, rpcClient *rpcclient.Client, ledgerWindow uint32) (uint32, uint32, error) {
	latestCheckpointLedger, err := GetLatestCheckpointLedger(ctx, rpcClient)
	if err != nil {
		return 0, 0, errors.Wrap(err, "failed to get latest checkpoint ledger")
	}

	return latestCheckpointLedger - ledgerWindow, latestCheckpointLedger, nil
}
