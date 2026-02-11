package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

var uniqueEventTopics map[string]struct{}

func SeedEventsData(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	writer *SeedWriter,
	parameters PreseedParameters,
) error {
	uniqueEventTopics = make(map[string]struct{})

	if err := writer.StartArray("contract_ids"); err != nil {
		return errors.Wrap(err, "failed to start contract_ids array")
	}

	limit := min(util.TxPageLimit, parameters.Range.Last-parameters.Range.First+1)

	for ; parameters.Range.First < parameters.Range.Last; parameters.Range.First += limit {
		endLedger := min(parameters.Range.First+limit-1, parameters.Range.Last)
		var cursor *protocol.Cursor

		for {
			// Fetch contract IDs using GetEvents
			req := util.MakeGetEventsRequest(parameters.Range.First, endLedger, cursor)
			eventsResponse, err := rpcClient.GetEvents(ctx, req)
			if err != nil {
				return errors.Wrapf(err, "failed to fetch event data for ledgers %d->%d",
					parameters.Range.First, endLedger)
			}

			for _, event := range eventsResponse.Events {
				if err := writer.WriteItem(event.ContractID); err != nil {
					return errors.Wrap(err, "failed to write contract ID item")
				}
				for _, topic := range event.TopicXDR {
					uniqueEventTopics[topic] = struct{}{}
				}
			}

			// No more pages when the server returns an empty cursor or no events.
			if eventsResponse.Cursor == "" || len(eventsResponse.Events) == 0 {
				break
			}
			c, err := protocol.ParseCursor(eventsResponse.Cursor)
			if err != nil {
				return errors.Wrapf(err, "failed to parse events cursor %q", eventsResponse.Cursor)
			}
			cursor = &c
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
