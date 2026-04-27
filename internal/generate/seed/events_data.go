package seed

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// EventDataSeeder collects per-contract event topic associations with a shared topic dictionary.
type EventDataSeeder struct {
	rpcClient      *rpcclient.Client
	uniqueTopics   []string // ordered list of unique topics
	contractEvents ContractEvents
}

func NewEventDataSeeder(rpcClient *rpcclient.Client) Seeder {
	return &EventDataSeeder{
		rpcClient:    rpcClient,
		uniqueTopics: make([]string, 0, util.DefaultSeedSliceSize),
		contractEvents: ContractEvents{
			ContractIds: make(map[string]*TopicData, util.DefaultSeedSliceSize),
		},
	}
}

// WriteResults writes the accumulated event data to the SeedWriter.
func (s *EventDataSeeder) WriteResults(w *SeedWriter) {
	w.ContractEventData = s.contractEvents
}

// SeedDataForRange implements Seeder for EventDataSeeder.
// It fetches events for the given ledger range and accumulates per-contract topic associations.
func (s *EventDataSeeder) SeedDataForRange(ctx context.Context, r Range) error {
	limit := min(util.DefaultEventsPageLimit, r.Last-r.First+1)

	for start := r.First; start <= r.Last; start += limit {
		endLedger := min(start+limit-1, r.Last)
		var cursor *protocol.Cursor

		for {
			req := util.MakeGetEventsRequest(start, endLedger, cursor)
			eventsResponse, err := s.rpcClient.GetEvents(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to fetch event data for ledgers %d->%d: %w",
					start, endLedger, err)
			}

			for _, event := range eventsResponse.Events {
				if uint32(event.Ledger) >= r.Last {
					return nil // events sorted ascending, nothing useful past here
				}
				s.contractEvents.AddEventData(event)
			}

			if eventsResponse.Cursor == "" || len(eventsResponse.Events) == 0 {
				break
			}
			c, err := protocol.ParseCursor(eventsResponse.Cursor)
			if err != nil {
				return fmt.Errorf("failed to parse events cursor %q: %w", eventsResponse.Cursor, err)
			}
			cursor = &c
		}
	}
	return nil
}

func (s *EventDataSeeder) String() string {
	return "event data"
}
