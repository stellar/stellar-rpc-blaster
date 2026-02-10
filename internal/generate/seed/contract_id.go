package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

func SeedContractIdData(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	writer *SeedWriter,
	parameters PreseedParameters,
) error {
	if err := writer.StartArray("contract_ids"); err != nil {
		return errors.Wrap(err, "failed to start contract_ids array")
	}

	limit := min(util.TxPageLimit, parameters.Range.Last-parameters.Range.First+1)

	for ; parameters.Range.First < parameters.Range.Last; parameters.Range.First += limit {
		req := protocol.GetEventsRequest{
			StartLedger: parameters.Range.First,
			EndLedger:   parameters.Range.First + limit - 1,
			Filters: []protocol.EventFilter{
				{
					EventType: protocol.EventTypeSet{
						protocol.EventTypeContract: true, // only matches on key, value is ignored
					},
				},
			},
			Format: "json",
		}
		eventsResponse, err := rpcClient.GetEvents(ctx, req)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch transaction data for ledgers %d->%d",
				parameters.Range.First, parameters.Range.First+limit-1)
		}
		for _, event := range eventsResponse.Events {
			writer.WriteItem(event.ContractID)
		}
	}

	if err := writer.EndArray(); err != nil {
		return errors.Wrap(err, "failed to end contract_ids array")
	}
	return nil
}
