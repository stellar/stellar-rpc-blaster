package seed

import (
	"context"

	"github.com/pkg/errors"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/collections/set"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// EventDataSeeder collects contract IDs and unique event topics.
type EventDataSeeder struct {
	contractIds  []string
	uniqueTopics set.Set[string]
}

func NewEventDataSeeder() *EventDataSeeder {
	return &EventDataSeeder{
		contractIds:  make([]string, 0, util.DefaultSeedSliceSize),
		uniqueTopics: set.NewSet[string](int(util.DefaultSeedSliceSize)),
	}
}

// ContractIDs returns the accumulated contract IDs.
func (s *EventDataSeeder) ContractIDs() []string {
	return s.contractIds
}

// EventTopics returns the accumulated unique event topics.
func (s *EventDataSeeder) EventTopics() []string {
	return s.uniqueTopics.Slice()
}

// SeedDataForRange implements Seeder for EventDataSeeder.
// It fetches events for the given ledger range and accumulates contract IDs AND unique topics.
func (s *EventDataSeeder) SeedDataForRange(
	ctx context.Context,
	_ *log.Entry,
	rpcClient *rpcclient.Client,
	r util.Range,
) error {
	limit := min(util.TxPageLimit, r.Last-r.First+1)

	for start := r.First; start <= r.Last; start += limit {
		endLedger := min(start+limit-1, r.Last)
		var cursor *protocol.Cursor

		for {
			req := util.MakeGetEventsRequest(start, endLedger, cursor)
			eventsResponse, err := rpcClient.GetEvents(ctx, req)
			if err != nil {
				return errors.Wrapf(err, "failed to fetch event data for ledgers %d->%d",
					start, endLedger)
			}

			for _, event := range eventsResponse.Events {
				s.contractIds = append(s.contractIds, event.ContractID)

				for _, topic := range event.TopicXDR {
					s.uniqueTopics.Add(topic)
				}
			}

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
	return nil
}
