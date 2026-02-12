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
) (uint32, error) {
	uniqueEventTopics = make(map[string]struct{})

	var contractIdCount uint32
	if err := writer.StartArray("contract_ids"); err != nil {
		return 0, errors.Wrap(err, "failed to start contract_ids array")
	}

	for _, r := range parameters.GetProcessingRanges() {
		if contractIdCountForRange, err := seedEventsForRange(ctx, rpcClient, writer, r); err != nil {
			return 0, err
		} else {
			contractIdCount += contractIdCountForRange
		}
	}

	if err := writer.EndArray(); err != nil {
		return 0, errors.Wrap(err, "failed to end contract_ids array")
	}
	return contractIdCount, nil
}

func seedEventsForRange(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	writer *SeedWriter,
	r Range,
) (uint32, error) {
	limit := min(util.TxPageLimit, r.Last-r.First+1)

	var contractIdCountForRange uint32
	for start := r.First; start <= r.Last; start += limit {
		endLedger := min(start+limit-1, r.Last)
		var cursor *protocol.Cursor

		for {
			req := util.MakeGetEventsRequest(start, endLedger, cursor)
			eventsResponse, err := rpcClient.GetEvents(ctx, req)
			if err != nil {
				return 0, errors.Wrapf(err, "failed to fetch event data for ledgers %d->%d",
					start, endLedger)
			}

			contractIdCountForRange += uint32(len(eventsResponse.Events))
			for _, event := range eventsResponse.Events {
				if err := writer.WriteItem(event.ContractID); err != nil {
					return 0, errors.Wrap(err, "failed to write contract ID item")
				}

				// Cache unique event topics for flushing after all ranges are processed
				for _, topic := range event.TopicXDR {
					uniqueEventTopics[topic] = struct{}{}
				}
			}

			if eventsResponse.Cursor == "" || len(eventsResponse.Events) == 0 {
				break
			}
			c, err := protocol.ParseCursor(eventsResponse.Cursor)
			if err != nil {
				return 0, errors.Wrapf(err, "failed to parse events cursor %q", eventsResponse.Cursor)
			}
			cursor = &c
		}
	}
	return contractIdCountForRange, nil
}

func FlushUniqueEventTopics(writer *SeedWriter) (uint32, error) {
	if err := writer.StartArray("event_topics"); err != nil {
		return 0, errors.Wrap(err, "failed to start event_topics array")
	}
	for topic := range uniqueEventTopics {
		if err := writer.WriteItem(topic); err != nil {
			return 0, errors.Wrap(err, "failed to write event topic item")
		}
	}
	if err := writer.EndArray(); err != nil {
		return 0, errors.Wrap(err, "failed to end event_topics array")
	}
	return uint32(len(uniqueEventTopics)), nil
}
