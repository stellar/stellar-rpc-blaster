package seed

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/collections/set"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// EventDataSeeder collects contract IDs and unique event topics.
type EventDataSeeder struct {
	rpcClient    *rpcclient.Client
	logger       *log.Entry
	contractIds  []string
	uniqueTopics set.Set[string]
}

func NewEventDataSeeder(rpcClient *rpcclient.Client, logger *log.Entry) *EventDataSeeder {
	return &EventDataSeeder{
		rpcClient:    rpcClient,
		logger:       logger,
		contractIds:  make([]string, 0, util.DefaultSeedSliceSize),
		uniqueTopics: set.NewSet[string](int(util.DefaultSeedSliceSize)),
	}
}

// ContractIds returns the accumulated contract IDs.
func (s *EventDataSeeder) ContractIds() []string {
	return s.contractIds
}

// EventTopics returns the accumulated unique event topics.
func (s *EventDataSeeder) EventTopics() []string {
	return s.uniqueTopics.Slice()
}

// SeedDataForRange implements Seeder for EventDataSeeder.
// It fetches events for the given ledger range and accumulates contract IDs AND unique topics.
func (s *EventDataSeeder) SeedDataForRange(ctx context.Context, r Range) error {
	limit := min(util.TxPageLimit, r.Last-r.First+1)

	for start := r.First; start <= r.Last; start += limit {
		endLedger := min(start+limit-1, r.Last)
		var cursor *protocol.Cursor

		for {
			req := util.MakeGetEventsRequest(start, endLedger, cursor)
			eventsResponse, err := s.rpcClient.GetEvents(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to fetch event data for ledgers %d->%d: %v",
					start, endLedger, err)
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
				return fmt.Errorf("failed to parse events cursor %q: %v", eventsResponse.Cursor, err)
			}
			cursor = &c
		}
	}
	return nil
}
