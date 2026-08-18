package parameters

import (
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
					symb("transfer"): {Count: 80, Params: [][]string{{symb("alice"), symb("bob")}}},
					symb("mint"):     {Count: 20, Params: [][]string{{symb("carol")}}},
				}},
			}},
			EmitterIds: []string{emitter, trimmedEmitter},
			LedgerKeys: keys,
		},
		Head: HeadInfo{Oldest: testOldest, Latest: testLatest},
	}
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
				// the SDK doesn't range-check events startLedger; catch placement underflow
				require.True(t, r.StartLedger >= testOldest && r.StartLedger <= testLatest, "%s", raw)
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
			bodies, err := BuildEndpointParams(endpoint, nModelBodies, params, 0)
			require.NoError(t, err)
			require.Len(t, bodies, nModelBodies)
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
			bodies, err = BuildEndpointParams(endpoint, 100, params, 77)
			require.NoError(t, err)
			for _, body := range bodies {
				require.EqualValues(t, map[string]any{"limit": uint32(77)}, body["pagination"],
					"configured limit must override the model's")
			}
		})
	}
}

// getEvents: the cold pool must never overlap any observed emitter — including
// ones trimmed out of the stored seed — or match rates silently inflate.
func eventsModelOk(t *testing.T, params *Parameters, _ []map[string]any) {
	s, err := newEventsSampler(params, rand.New(rand.NewPCG(1, 2)))
	require.NoError(t, err)
	require.NotEmpty(t, s.cold)
	for _, cold := range s.cold {
		require.NotContains(t, params.Output.EmitterIds, cold)
	}
}

// TestEndpointModelTinyWindow pins the saturating placement: no uint32 wraparound
// or below-floor starts when the ledger window is smaller than every placement band.
func TestEndpointModelTinyWindow(t *testing.T) {
	params := modelParams(t)
	params.Head = HeadInfo{Oldest: 1, Latest: 5}
	for _, endpoint := range []string{"getEvents", "getTransactions", "getLedgers"} {
		bodies, err := BuildEndpointParams(endpoint, 500, params, 0)
		require.NoError(t, err, endpoint)
		for _, body := range bodies {
			start := body["startLedger"].(uint32)
			require.True(t, start >= 1 && start <= 5, "%s startLedger %d out of window", endpoint, start)
		}
	}
}

// getTransaction: seen-before and never-land shares are emergent — repolls compound
// with dead-hash pool reuse — so pin the composition, not the raw parameters.
func getTransactionModelOk(t *testing.T, params *Parameters, bodies []map[string]any) {
	seedSet := make(map[string]bool, len(params.Output.TxHashes))
	for _, h := range params.Output.TxHashes {
		seedSet[h] = true
	}
	seen := map[string]bool{}
	repolls, notFound := 0.0, 0.0
	for _, body := range bodies {
		hash := body["hash"].(string)
		if seen[hash] {
			repolls++
		}
		seen[hash] = true
		if !seedSet[hash] {
			notFound++
		}
	}
	seenBefore := util.PrTxRepoll + (1-util.PrTxRepoll)*util.PrTxNotFound
	require.InDelta(t, seenBefore, repolls/nModelBodies, 0.03)
	require.InDelta(t, util.PrTxNotFound, notFound/nModelBodies, 0.02)
}
