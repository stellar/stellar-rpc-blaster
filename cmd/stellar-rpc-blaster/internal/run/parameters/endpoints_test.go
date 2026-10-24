package parameters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

const (
	testOldest   uint32 = 1_000_000
	testLatest   uint32 = 1_121_000
	nModelBodies        = 10_000
)

var testHeadRange = protocol.LedgerSeqRange{FirstLedger: testOldest, LastLedger: testLatest}

func testContractID(t *testing.T, b byte) (string, string) {
	var raw [32]byte
	raw[0] = b
	cid := xdr.ContractId(raw)
	key := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract:   xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &cid},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}
	keyB64, err := xdr.MarshalBase64(key)
	require.NoError(t, err)
	return strkey.MustEncode(strkey.VersionByteContract, raw[:]), keyB64
}

// modelParams returns seed + live-head fixtures usable by every modeled endpoint.
// The hash pool is large so fresh-draw collisions don't pollute the repoll measurement.
func modelParams(t *testing.T) *Parameters {
	symb := func(s string) string {
		sym := xdr.ScSymbol(s)
		b64, err := xdr.MarshalBase64(xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym})
		require.NoError(t, err)
		return b64
	}
	hashes := make([]string, 100_000)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i+1)
	}
	addr := func(b byte) string {
		var raw xdr.Uint256
		raw[0] = b
		b64, err := xdr.MarshalBase64(xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &raw},
		}})
		require.NoError(t, err)
		return b64
	}
	emitter, _ := testContractID(t, 0xE1)
	keys := make([]string, 60)
	for i := range keys {
		_, keys[i] = testContractID(t, byte(i+1))
	}
	trimmedEmitter, _ := testContractID(t, 0x01) // present in keys, trimmed out of contract_events
	return &Parameters{
		Output: seed.SeedData{
			TxHashes: hashes,
			ContractEventData: seed.ContractEvents{ContractIds: map[string]*seed.TopicData{
				emitter: {Count: 100, Topic: map[string]*seed.ParamTopics{
					symb("transfer"): {Count: 80, Params: [][]string{{addr(0xAA), addr(0xAB), symb("amount")}}},
					symb("mint"):     {Count: 20, Params: [][]string{{symb("carol")}}},
				}},
			}},
			EmitterIds: []string{emitter, trimmedEmitter},
			LedgerKeys: keys,
		},
		Head: HeadInfo{Oldest: testOldest, Latest: testLatest},
	}
}

// TestEndpointParamsReproducible checks the run-level seed fully determines every
// data endpoint's bodies, and that endpoints draw independent streams from that one
// seed rather than opening on the same ledger and warming each other's caches.
func TestEndpointParamsReproducible(t *testing.T) {
	build := func(endpoint string, seed uint64, n int) []map[string]any {
		params := modelParams(t)
		params.RngSeed = seed
		bodies, _, err := BuildEndpointParams(endpoint, n, params, 0)
		require.NoError(t, err)
		return bodies
	}
	for _, endpoint := range []string{"getTransaction", "getLedgerEntries", "getTransactions", "getLedgers", "getEvents", "queryEvents"} {
		t.Run(endpoint, func(t *testing.T) {
			require.Equal(t, build(endpoint, 12345, 200), build(endpoint, 12345, 200), "one seed must replay identically")
			require.NotEqual(t, build(endpoint, 12345, 200), build(endpoint, 999, 200), "the seed must actually drive the draws")
		})
	}
	require.NotEqual(t, build("getTransactions", 777, 20)[0]["startLedger"], build("getLedgers", 777, 20)[0]["startLedger"],
		"head-anchored endpoints must draw independent streams from the same root seed")

	labels := func(endpoint string) []string {
		params := modelParams(t)
		params.RngSeed = 777
		_, l, err := BuildEndpointParams(endpoint, 200, params, 0)
		require.NoError(t, err)
		return l
	}
	require.NotEqual(t, labels("getEvents"), labels("queryEvents"),
		"the events endpoints must draw independent streams, never replaying each other's requests")
}

// TestEndpointModel checks, for every modeled endpoint: each body passes the SDK's
// server-side validation, a configured limit overrides the model's own, and the
// model-specific properties that aren't direct parameter echoes hold.
func TestEndpointModel(t *testing.T) {
	models := map[string]struct {
		validate func(t *testing.T, raw []byte)
		modelOk  func(t *testing.T, params *Parameters, bodies []map[string]any)
	}{
		"getEvents": {
			validate: func(t *testing.T, raw []byte) {
				var r protocol.GetEventsRequest
				require.NoError(t, json.Unmarshal(raw, &r), "%s", raw)
				require.NoError(t, r.Valid(uint(util.MaxEventsPageLimit)), "%s", raw)
				// the SDK doesn't range-check events startLedger so check placement underflow
				require.True(t, r.StartLedger >= testOldest && r.StartLedger <= testLatest, "%s", raw)
			},
			modelOk: eventsModelOk,
		},
		"queryEvents": {
			validate: func(t *testing.T, raw []byte) {
				var r protocol.QueryEventsRequest
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.DisallowUnknownFields() // as the server decodes
				require.NoError(t, dec.Decode(&r), "%s", raw)
				require.NoError(t, r.Valid(protocol.QueryEventsDefaultMaxFilters), "%s", raw)
				// the SDK doesn't range-check minLedger so check placement underflow
				require.True(t, r.MinLedger >= testOldest && r.MinLedger <= testLatest, "%s", raw)
			},
			modelOk: eventsModelOk,
		},
		"getTransaction": {
			validate: func(t *testing.T, raw []byte) {
				var r protocol.GetTransactionRequest
				require.NoError(t, json.Unmarshal(raw, &r), "%s", raw)
				require.Regexp(t, "^[0-9a-f]{64}$", r.Hash) // no SDK validator exists for this one
			},
			modelOk: getTransactionModelOk,
		},
		"getTransactions": {
			validate: func(t *testing.T, raw []byte) {
				var r protocol.GetTransactionsRequest
				require.NoError(t, json.Unmarshal(raw, &r), "%s", raw)
				require.NoError(t, r.IsValid(uint(util.MaxTxPageLimit), testHeadRange), "%s", raw)
			},
		},
		"getLedgers": {
			validate: func(t *testing.T, raw []byte) {
				var r protocol.GetLedgersRequest
				require.NoError(t, json.Unmarshal(raw, &r), "%s", raw)
				require.NoError(t, r.Validate(uint(util.MaxLedgersPageLimit), testHeadRange), "%s", raw)
			},
		},
	}
	for endpoint, m := range models {
		t.Run(endpoint, func(t *testing.T) {
			params := modelParams(t)
			bodies, labels, err := BuildEndpointParams(endpoint, nModelBodies, params, 0)
			require.NoError(t, err)
			require.Len(t, bodies, nModelBodies)
			if HasArchetypes(endpoint) { // per-archetype labels attribute events results
				require.Len(t, labels, nModelBodies)
				require.Subset(t, EventsArchetypeNames(), labels)
			} else {
				require.Nil(t, labels)
			}
			for _, body := range bodies {
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				m.validate(t, raw)
			}
			if m.modelOk != nil {
				m.modelOk(t, params, bodies)
			}

			if endpoint == "getTransaction" {
				return // nothing paginated to override
			}
			bodies, _, err = BuildEndpointParams(endpoint, 100, params, 77)
			require.NoError(t, err)
			for _, body := range bodies {
				require.EqualValues(t, 77, limitOf(body), "configured limit must override the model's")
			}
		})
	}
}

// getEvents: the cold pool must never overlap any observed emitter or match rates inflate.
func eventsModelOk(t *testing.T, params *Parameters, _ []map[string]any) {

	s, err := newEventsSampler(params, rand.New(rand.NewPCG(1, 2)))
	require.NoError(t, err)
	require.NotEmpty(t, s.cold)
	for _, cold := range s.cold {
		require.NotContains(t, params.Output.EmitterIds, cold)
	}
	// wallet values must be account addresses, not arbitrary observed params
	require.Len(t, s.wallets, 2)
	for _, w := range s.wallets {
		var v xdr.ScVal
		require.NoError(t, xdr.SafeUnmarshalBase64(w, &v))
		require.Equal(t, xdr.ScValTypeScvAddress, v.Type)
	}
}

// TestEndpointModelWindow checks the head's window bounds every placement: starts within
// [Floor, Latest] and no requested window reaching past Latest, both when narrowed to a
// query window and when tiny (no uint32 wraparound or below-floor starts when the window
// is smaller than every placement band).
func TestEndpointModelWindow(t *testing.T) {
	narrowed := modelParams(t).Head
	require.NoError(t, narrowed.Narrow([]uint32{testOldest + 50_000, testLatest - 20_000}))
	for name, head := range map[string]HeadInfo{"narrowed": narrowed, "tiny": {Oldest: 1, Latest: 5}} {
		t.Run(name, func(t *testing.T) {
			params := modelParams(t)
			params.Head = head
			floor, top := head.Floor(), head.Latest
			for _, endpoint := range []string{"getEvents", "queryEvents", "getTransactions", "getLedgers"} {
				bodies, _, err := BuildEndpointParams(endpoint, nModelBodies, params, 0)
				require.NoError(t, err, endpoint)
				for _, body := range bodies {
					start := startOf(body)
					require.True(t, start >= floor && start <= top, "%s start %d outside [%d, %d]", endpoint, start, floor, top)
					if end, ok := body["endLedger"].(uint32); ok {
						require.LessOrEqual(t, end, top+1, "%s endLedger is exclusive", endpoint)
					}
					if end, ok := body["maxLedger"].(uint32); ok {
						require.LessOrEqual(t, end, top, endpoint)
					}
				}
			}
		})
	}
}

// startOf returns a body's first ledger, whichever method's params shape it has.
func startOf(body map[string]any) uint32 {
	if start, ok := body["minLedger"].(uint32); ok { // queryEvents
		return start
	}
	return body["startLedger"].(uint32)
}

// limitOf returns a body's page limit, whichever method's params shape it has.
func limitOf(body map[string]any) any {
	if p, ok := body["pagination"].(map[string]any); ok {
		return p["limit"]
	}
	return body["limit"] // queryEvents
}

// getTransaction: pin the never-land share, the one distributional knob of the hash stream.
func getTransactionModelOk(t *testing.T, params *Parameters, bodies []map[string]any) {
	seedSet := make(map[string]bool, len(params.Output.TxHashes))
	for _, h := range params.Output.TxHashes {
		seedSet[h] = true
	}
	notFound := 0.0
	for _, body := range bodies {
		if !seedSet[body["hash"].(string)] {
			notFound++
		}
	}
	require.InDelta(t, util.PrTxNotFound, notFound/nModelBodies, 0.02)
}

// TestEventsRenderers pins how one sampled events query renders for each method:
// the model sweep proves bodies valid, this proves the two renderings equivalent.
func TestEventsRenderers(t *testing.T) {
	cases := map[string]struct {
		q                eventsQuery
		getEvents, query map[string]any
	}{
		"bounded, value + wildcard topic": {
			q: eventsQuery{start: 100, end: 351, limit: 200, json: true,
				filters: []eventsFilter{{contractIDs: []string{"CA"}, topic: []string{"V0", "*", "V2"}}}},
			getEvents: map[string]any{"startLedger": uint32(100), "endLedger": uint32(351), "xdrFormat": "json",
				"pagination": map[string]any{"limit": uint(200)},
				"filters":    []map[string]any{{"type": "contract", "contractIds": []string{"CA"}, "topics": [][]string{{"V0", "*", "V2"}}}}},
			query: map[string]any{"minLedger": uint32(100), "maxLedger": uint32(350), "xdrFormat": "json", "limit": uint(200),
				"filters": []map[string]any{{"type": "contract", "contractId": "CA", "topic0": "V0", "topic2": "V2"}}},
		},
		"one contract, no topic, one-ledger window": {
			q: eventsQuery{start: 500, end: 501, limit: 100, filters: []eventsFilter{{contractIDs: []string{"CA"}}}},
			getEvents: map[string]any{"startLedger": uint32(500), "endLedger": uint32(501),
				"pagination": map[string]any{"limit": uint(100)},
				"filters":    []map[string]any{{"type": "contract", "contractIds": []string{"CA"}}}},
			query: map[string]any{"minLedger": uint32(500), "maxLedger": uint32(500), "limit": uint(100),
				"filters": []map[string]any{{"type": "contract", "contractId": "CA"}}},
		},
		"two contracts, ** tail": {
			q: eventsQuery{start: 100, limit: 100,
				filters: []eventsFilter{{contractIDs: []string{"CA", "CB"}, topic: []string{"W0", "**"}}}},
			getEvents: map[string]any{"startLedger": uint32(100), "pagination": map[string]any{"limit": uint(100)},
				"filters": []map[string]any{{"type": "contract", "contractIds": []string{"CA", "CB"}, "topics": [][]string{{"W0", "**"}}}}},
			query: map[string]any{"minLedger": uint32(100), "limit": uint(100), "filters": []map[string]any{
				{"type": "contract", "contractId": "CA", "topic0": "W0"},
				{"type": "contract", "contractId": "CB", "topic0": "W0"},
			}},
		},
		"topics-only (transfer watcher)": {
			q: eventsQuery{start: 100, limit: 100, filters: []eventsFilter{
				{topic: []string{"T", "A", "*", "*"}},
				{topic: []string{"T", "*", "A", "*"}},
			}},
			getEvents: map[string]any{"startLedger": uint32(100), "pagination": map[string]any{"limit": uint(100)}, "filters": []map[string]any{
				{"type": "contract", "topics": [][]string{{"T", "A", "*", "*"}}},
				{"type": "contract", "topics": [][]string{{"T", "*", "A", "*"}}},
			}},
			query: map[string]any{"minLedger": uint32(100), "limit": uint(100), "filters": []map[string]any{
				{"type": "contract", "topic0": "T", "topic1": "A"},
				{"type": "contract", "topic0": "T", "topic2": "A"},
			}},
		},
		"firehose: open-ended, unfiltered": {
			q:         eventsQuery{start: 7, limit: 1},
			getEvents: map[string]any{"startLedger": uint32(7), "pagination": map[string]any{"limit": uint(1)}},
			query:     map[string]any{"minLedger": uint32(7), "limit": uint(1)},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, c.getEvents, renderGetEvents(c.q))
			require.Equal(t, c.query, renderQueryEvents(c.q))
		})
	}
}
