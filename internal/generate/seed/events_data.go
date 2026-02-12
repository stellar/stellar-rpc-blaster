package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/collections/set"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type EventTopics struct {
	Topics []string `json:"event_topics"`
}
type ContractIDs struct {
	IDs []string `json:"contract_ids"`
}

var uniqueEventTopics set.Set[string]

// Bootstraps contract ID and unique event topic data in the given range(s)
func SeedEventsData(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	writer *SeedWriter,
	parameters PreseedParameters,
) (uint32, uint32, error) {
	uniqueEventTopics = set.NewSet[string](util.DefaultSeedSliceSize)
	contractIdEntry := NewEntry[string]("contract_ids", util.DefaultSeedSliceSize)
	eventTopicEntry := NewEntry[string]("event_topics", util.DefaultSeedSliceSize)

	var contractIdCount uint32
	// if err := writer.StartArray("contract_ids"); err != nil {
	// 	return 0, errors.Wrap(err, "failed to start contract_ids array")
	// }

	for _, r := range parameters.GetProcessingRanges() {
		if contractIdCountForRange, err := seedEventDataForRange(ctx, rpcClient, &contractIdEntry, r); err != nil {
			return 0, 0, err
		} else {
			contractIdCount += contractIdCountForRange
		}
	}

	eventTopicEntry.Map["event_topics"] = uniqueEventTopics.Slice()
	if err := writer.FlushMap(eventTopicEntry.Map); err != nil {
		return 0, 0, errors.Wrap(err, "failed to flush event topics")
	}
	if err := writer.FlushMap(contractIdEntry.Map); err != nil {
		return 0, 0, errors.Wrap(err, "failed to flush contract IDs")
	}

	return contractIdCount, uint32(len(uniqueEventTopics)), nil
}

func seedEventDataForRange(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	contractIdEntry *Entry[string],
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
				contractIdEntry.Append(event.ContractID)

				// Cache unique event topics for flushing after all ranges are processed
				for _, topic := range event.TopicXDR {
					uniqueEventTopics.Add(topic)
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
