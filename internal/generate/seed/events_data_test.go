package seed

import (
	"encoding/json"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
	"github.com/stretchr/testify/require"
)

func newTestEventDataSeeder(client *rpcclient.Client) *EventDataSeeder {
	return &EventDataSeeder{
		rpcClient:    client,
		uniqueTopics: make([]string, 0, util.DefaultSeedSliceSize),
		contractEvents: ContractEvents{
			ContractIds: make(map[string]*TopicData, util.DefaultSeedSliceSize),
		},
	}
}

func stubEvent(ledger int32, contractID, topic, param string) protocol.EventInfo {
	return protocol.EventInfo{
		EventType:  protocol.EventTypeContract,
		Ledger:     ledger,
		ContractID: contractID,
		TopicXDR:   []string{topic, param},
	}
}

// captureEvents wraps NewMockRPCClient with the boilerplate every events test
// needs: assert the method is getEvents, decode into a GetEventsRequest, append
// to the captured slice, and delegate the canned response to `respond`. The
// returned accessor returns the current slice each time it's called — read it
// after the seeder has finished running.
func captureEvents(t *testing.T, respond func() protocol.GetEventsResponse) (*rpcclient.Client, func() []protocol.GetEventsRequest) {
	var calls []protocol.GetEventsRequest
	client := util.NewMockRPCClient(t, func(method string, params json.RawMessage) any {
		require.Equal(t, protocol.GetEventsMethodName, method)
		var req protocol.GetEventsRequest
		require.NoError(t, json.Unmarshal(params, &req))
		calls = append(calls, req)
		return respond()
	})
	return client, func() []protocol.GetEventsRequest { return calls }
}

// TestEventDataSeeder_SingleLedger verifies that the events seeder finds exactly one ledger given
// the one-ledger range {N, N}
func TestEventDataSeeder_SingleLedger(t *testing.T) {
	client, calls := captureEvents(t, func() protocol.GetEventsResponse { return protocol.GetEventsResponse{} })
	seeder := newTestEventDataSeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 100, Last: 100}))

	got := calls()
	require.Len(t, got, 1, "expected exactly one getEvents call for a single-ledger range")
	require.EqualValues(t, 100, got[0].StartLedger)
	require.EqualValues(t, 101, got[0].EndLedger,
		"endLedger must be exclusive (startLedger+1) so ledger N is actually queried")
}

// TestEventDataSeeder_MultiLedgerSinglePage verifies that a range smaller than
// DefaultDefaultEventsPageLimit produces one getEvents call covering [First, Last+1).
func TestEventDataSeeder_MultiLedgerSinglePage(t *testing.T) {
	client, calls := captureEvents(t, func() protocol.GetEventsResponse { return protocol.GetEventsResponse{} })
	seeder := newTestEventDataSeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 200, Last: 249}))

	got := calls()
	require.Len(t, got, 1, "a 50-ledger range fits in one DefaultDefaultEventsPageLimit chunk")
	require.EqualValues(t, 200, got[0].StartLedger)
	require.EqualValues(t, 250, got[0].EndLedger,
		"endLedger = Last+1 so the final ledger is included under RPC exclusive-end semantics")
}

// TestEventDataSeeder_ChunksAcrossPages verifies that a range larger than
// the page limit is split into precisely contiguous getEvents calls
func TestEventDataSeeder_ChunksAcrossPages(t *testing.T) {
	client, calls := captureEvents(t, func() protocol.GetEventsResponse { return protocol.GetEventsResponse{} })
	seeder := newTestEventDataSeeder(client)

	// 250 inclusive ledgers -> 3 chunks under DefaultDefaultEventsPageLimit=100: [1000,1100), [1100,1200), [1200,1250).
	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 1000, Last: 1249}))

	got := calls()
	require.Len(t, got, 3)
	require.EqualValues(t, 1000, got[0].StartLedger)
	require.EqualValues(t, 1100, got[0].EndLedger)
	require.EqualValues(t, 1100, got[1].StartLedger)
	require.EqualValues(t, 1200, got[1].EndLedger)
	require.EqualValues(t, 1200, got[2].StartLedger)
	require.EqualValues(t, 1250, got[2].EndLedger)
}

// TestEventDataSeeder_AccumulatesEvents verifies that events returned by the RPC flow into
// the seeder's ContractEvents accumulator, keyed by contract ID and topic.
func TestEventDataSeeder_AccumulatesEvents(t *testing.T) {
	const (
		contractA = "contract-A"
		contractB = "contract-B"
		topic     = "topic-T"
	)
	events := []protocol.EventInfo{
		stubEvent(300, contractA, topic, "pa"),
		stubEvent(300, contractA, topic, "pa2"),
		stubEvent(301, contractB, topic, "pb"),
	}
	client, _ := captureEvents(t, func() protocol.GetEventsResponse {
		return protocol.GetEventsResponse{Events: events}
	})
	seeder := newTestEventDataSeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 300, Last: 309}))

	require.Contains(t, seeder.contractEvents.ContractIds, contractA)
	require.Contains(t, seeder.contractEvents.ContractIds, contractB)
	require.Len(t, seeder.contractEvents.ContractIds[contractA].Topic[topic].Params, 2)
	require.Len(t, seeder.contractEvents.ContractIds[contractB].Topic[topic].Params, 1)
}

// TestEventDataSeeder_FollowsCursor verifies the inner pagination loop by ensuring
// that when seeding two ranges, the first range-based request correctly returns a
// cursor that's used by the second request to exhaust the rest of the range
func TestEventDataSeeder_FollowsCursor(t *testing.T) {
	firstCursor := protocol.Cursor{Ledger: 400, Tx: 1, Op: 1, Event: 1}
	call := 0
	client, calls := captureEvents(t, func() protocol.GetEventsResponse {
		call++
		if call == 1 {
			return protocol.GetEventsResponse{
				Events: []protocol.EventInfo{stubEvent(400, "contract-A", "topic-T", "p")},
				Cursor: firstCursor.String(),
			}
		}
		return protocol.GetEventsResponse{} // empty events → loop exits
	})
	seeder := newTestEventDataSeeder(client)

	require.NoError(t, seeder.SeedDataForRange(t.Context(), Range{First: 400, Last: 400}))

	got := calls()
	require.Len(t, got, 2, "expected one initial call plus one cursor-driven follow-up")

	// First call: range-scoped, no cursor.
	require.EqualValues(t, 400, got[0].StartLedger)
	require.EqualValues(t, 401, got[0].EndLedger)
	require.NotNil(t, got[0].Pagination)
	require.Nil(t, got[0].Pagination.Cursor)

	// Second call: cursor-driven; MakeGetEventsRequest zeroes the ledger fields.
	require.EqualValues(t, 0, got[1].StartLedger)
	require.EqualValues(t, 0, got[1].EndLedger)
	require.NotNil(t, got[1].Pagination)
	require.NotNil(t, got[1].Pagination.Cursor)
	require.Equal(t, firstCursor, *got[1].Pagination.Cursor)
}
