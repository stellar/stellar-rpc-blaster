package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

var uniqueEventTopics = make(map[string]struct{})

func SeedEventsData(
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
		}
		eventsResponse, err := rpcClient.GetEvents(ctx, req)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch transaction data for ledgers %d->%d",
				parameters.Range.First, parameters.Range.First+limit-1)
		}
		for _, event := range eventsResponse.Events {
			writer.WriteItem(event.ContractID)
			// Populate unique event topics map along the way since it's not much data to cache
			for _, topic := range event.TopicXDR {
				uniqueEventTopics[topic] = struct{}{}
			}
		}
	}

	if err := writer.EndArray(); err != nil {
		return errors.Wrap(err, "failed to end contract_ids array")
	}
	return nil
}

func FlushUniqueEventTopics(writer *SeedWriter) error {
	if err := writer.StartArray("event_topics"); err != nil {
		return errors.Wrap(err, "failed to start event_topics array")
	}
	for topic := range uniqueEventTopics {
		if err := writer.WriteItem(topic); err != nil {
			return errors.Wrap(err, "failed to write event topic item")
		}
	}
	if err := writer.EndArray(); err != nil {
		return errors.Wrap(err, "failed to end event_topics array")
	}
	return nil
}
