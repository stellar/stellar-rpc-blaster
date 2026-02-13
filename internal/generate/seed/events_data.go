package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/collections/set"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

var uniqueEventTopics set.Set[string]

// Bootstraps contract ID and unique event topic data in the given range(s)
func SeedEventsData(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	parameters util.PreseedParameters,
) ([]string, []string, error) {
	uniqueEventTopics = set.NewSet[string](int(util.DefaultSeedSliceSize))
	contractIdEntry := NewEntry[string]("contract_ids", util.DefaultSeedSliceSize)

	for _, r := range parameters.GetProcessingRanges() {
		if _, err := seedEventDataForRange(ctx, rpcClient, &contractIdEntry, r); err != nil {
			return nil, nil, err
		}
	}

	return contractIdEntry.Slice(), uniqueEventTopics.Slice(), nil
}

func seedEventDataForRange(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	contractIdEntry *Entry[string],
	r util.Range,
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
