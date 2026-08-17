package parameters

import (
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/generate/seed"
	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

// The getEvents traffic model is a weighted mixture of behavioral archetypes observed
// in prod traffic Each archetype owns its joint window x filter x limit shape.
var eventsArchetypes = []eventsArchetype{
	{"head-poll", 0.48, (*eventsSampler).headPoll},
	{"deep-pager", 0.23, (*eventsSampler).deepPager},
	{"tail-poll", 0.12, (*eventsSampler).tailPoll},
	{"transfer-watcher", 0.08, (*eventsSampler).transferWatcher},
	{"catch-up", 0.04, (*eventsSampler).catchUp},
	{"deep-scan", 0.03, (*eventsSampler).deepScan},
	{"firehose", 0.02, (*eventsSampler).firehose},
}

// Single-topic wildcard shapes and weights among topic-carrying filters
// (V = observed segment, * = single-segment wildcard).
var (
	eventsTopicShapes       = []string{"V", "V*", "VV**", "V*V*"}
	eventsTopicShapeWeights = []float64{0.27, 0.19, 0.27, 0.27}
)

type eventsArchetype struct {
	name   string
	weight float64
	build  func(*eventsSampler) map[string]any
}

// eventsSampler holds the pools and live-head anchor that archetype builders draw from.
type eventsSampler struct {
	rng            *rand.Rand
	head           HeadInfo
	emitters       []string
	emitterWeights []float64
	events         seed.ContractEvents
	cold           []string
	coldWeights    []float64
	paramPool      []string // observed ScVal params, for realistic rare topic values
	transferSym    string   // ScSymbol("transfer"), base64
	limitOverride  uint32
	weights        []float64
}

// chooseOne draws one item with probability weights[i]/sum(weights).
func chooseOne[T any](rng *rand.Rand, items []T, weights []float64) T {
	return util.WeightedChooseNSeeded(items, weights, 1, rng)[0]
}

func newEventsSampler(params *Parameters, limitOverride uint32, rng *rand.Rand) (*eventsSampler, error) {
	if params == nil || params.Head.Latest == 0 {
		return nil, fmt.Errorf("getEvents sampler needs seed data and a preflight-captured ledger head")
	}
	emitters, emitterWeights := params.Output.ContractEventData.ContractsAndWeights()
	if len(emitters) == 0 {
		return nil, fmt.Errorf("seed data contains no contract events")
	}
	cold := coldPoolFromKeys(params.Output.LedgerKeys, emitters, util.EventsColdPoolSize)
	if len(cold) == 0 {
		return nil, fmt.Errorf("seed data contains no contract-data ledger keys for the events cold pool — rerun generate")
	}
	// Zipf-ish reuse weights: real cold pollers concentrate on a few contracts
	coldWeights := make([]float64, len(cold))
	for i := range coldWeights {
		coldWeights[i] = 1 / float64(i+1)
	}
	sym := xdr.ScSymbol("transfer")
	transferSym, err := xdr.MarshalBase64(xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym})
	if err != nil {
		return nil, err
	}
	s := &eventsSampler{
		rng:            rng,
		head:           params.Head,
		emitters:       emitters,
		emitterWeights: emitterWeights,
		events:         params.Output.ContractEventData,
		cold:           cold,
		coldWeights:    coldWeights,
		paramPool:      collectParams(params.Output.ContractEventData),
		transferSym:    transferSym,
		limitOverride:  limitOverride,
		weights:        make([]float64, len(eventsArchetypes)),
	}
	for i, a := range eventsArchetypes {
		s.weights[i] = a.weight
	}
	return s, nil
}

// sample draws one archetype and builds its request params map, returning the
// archetype name for tests and histograms.
func (s *eventsSampler) sample() (string, map[string]any) {
	a := chooseOne(s.rng, eventsArchetypes, s.weights)
	body := a.build(s)
	if s.limitOverride != 0 {
		body["pagination"] = map[string]any{"limit": s.limitOverride}
	}
	if s.rng.Float64() < util.PrEventsJson {
		body["xdrFormat"] = "json"
	}
	return a.name, body
}

// ---- archetype builders ----

// headPoll: start at head/head-1 and query a mix of open-ended + one-ledger windows.
// mostly cold contracts with a follower-on-emitter slice that catches fresh events.
func (s *eventsSampler) headPoll() map[string]any {
	start := s.head.Latest - chooseOne(s.rng, []uint32{0, 1}, []float64{0.47, 0.53})
	body := s.body(start, s.contractFilter(0.10, 0.25))
	if s.rng.Float64() < 0.5 {
		body["endLedger"] = start + 1 // one-ledger window case
	}
	s.setLimit(body, []uint{100, 200}, []float64{0.75, 0.25})
	return body
}

// deepPager: poll deep-placed 250-ledger window with a fatter tail (measured avg
// window 421), limit 1000. polls mostly cold pages and rarely emitters or topics
func (s *eventsSampler) deepPager() map[string]any {
	start := s.placeDeep(0)
	body := s.body(start, s.contractFilter(0.06, 0))
	span := chooseOne(s.rng, []uint32{250, 2000}, []float64{0.9, 0.1})
	body["endLedger"] = min(start+span, s.head.Latest)
	s.setLimit(body, []uint{1000}, []float64{1})
	return body
}

// tailPoll: mid-band pollers 10-10k behind, mostly bounded windows. carries the
// topic-bearing and two-contract minority of the poll traffic. Open-ended depth is
// bimodal per the measured mid-band.
func (s *eventsSampler) tailPoll() map[string]any {
	bounded := s.rng.Float64() < 0.70
	var depth uint32
	switch {
	case bounded:
		depth = 10 + uint32(s.rng.IntN(int(util.EventsDeepBandFloor)-10))
	case s.rng.Float64() < 0.45:
		depth = 10 + uint32(s.rng.IntN(240))
	default:
		depth = 1000 + uint32(s.rng.IntN(4000))
	}
	start := s.clampStart(s.head.Latest - depth)
	filter := s.contractFilter(0, 1.0/3)
	if s.rng.Float64() < 0.03 {
		filter["contractIds"] = []string{s.coldContract(), s.coldContract()}
	}
	body := s.body(start, filter)
	if bounded {
		span := chooseOne(s.rng, []uint32{120, 250, 590}, []float64{0.3, 0.5, 0.2})
		body["endLedger"] = min(start+span, s.head.Latest)
	}
	s.setLimit(body, []uint{100, 200}, []float64{0.6, 0.4})
	return body
}

// transferWatcher: the wallet watcher — two topics-only filters matching a transfer
// where the wallet is sender or receiver; open-ended tail-follow from head.
func (s *eventsSampler) transferWatcher() map[string]any {
	start := s.head.Latest - uint32(s.rng.IntN(3))
	wallet := s.paramValue()
	body := map[string]any{
		"startLedger": start,
		"filters": []map[string]any{
			{"type": "contract", "topics": [][]string{{s.transferSym, "*", wallet, "*"}}},
			{"type": "contract", "topics": [][]string{{s.transferSym, wallet, "*", "*"}}},
		},
	}
	s.setLimit(body, []uint{100}, []float64{1})
	return body
}

// catchUp: tuned-window catch-up reads on real emitters; the matching minority.
func (s *eventsSampler) catchUp() map[string]any {
	start := s.clampStart(s.head.Latest - uint32(10+s.rng.IntN(240)))
	body := s.body(start, map[string]any{"type": "contract", "contractIds": []string{s.emitterContract()}})
	s.setLimit(body, []uint{100, 200}, []float64{0.7, 0.3})
	return body
}

// deepScan: open-ended scans from the retention floor (with a ~30k-deep secondary
// scan type); the server walks the window to head finding nothing.
func (s *eventsSampler) deepScan() map[string]any {
	var start uint32
	if s.rng.Float64() < 0.85 {
		start = s.clampStart(s.head.Oldest + util.EventsLeftEdgeMargin + uint32(s.rng.IntN(1000)))
	} else {
		start = s.placeDeep(30_000)
	}
	body := s.body(start, s.contractFilter(0, 0))
	if s.rng.Float64() < 0.06 {
		delete(body, "filters")
	}
	s.setLimit(body, []uint{100, 1000}, []float64{0.5, 0.5})
	return body
}

// firehose: no filters at all. matches everything and early-exits at limit.
func (s *eventsSampler) firehose() map[string]any {
	start := s.head.Latest - uint32(s.rng.IntN(2))
	body := map[string]any{"startLedger": start}
	s.setLimit(body, []uint{1, 100, 200}, []float64{0.85, 0.075, 0.075})
	return body
}

// ---- shared helpers ----

func (s *eventsSampler) body(start uint32, filter map[string]any) map[string]any {
	return map[string]any{
		"startLedger": start,
		"filters":     []map[string]any{filter},
	}
}

// contractFilter builds a single-contract filter: cold by default, emitter with
// probability prEmitter, single wildcard-shaped topic with probability prTopic.
func (s *eventsSampler) contractFilter(prEmitter, prTopic float64) map[string]any {
	cid := s.coldContract()
	if s.rng.Float64() < prEmitter {
		cid = s.emitterContract()
	}
	filter := map[string]any{"type": "contract", "contractIds": []string{cid}}
	if s.rng.Float64() < prTopic {
		filter["topics"] = [][]string{s.shapedTopic()}
	}
	return filter
}

// shapedTopic overlays a measured wildcard shape on a real observed topic vector.
func (s *eventsSampler) shapedTopic() []string {
	vec := s.topicVector()
	shape := chooseOne(s.rng, eventsTopicShapes, eventsTopicShapeWeights)
	topic := make([]string, len(shape))
	for i := range topic {
		if shape[i] == 'V' && i < len(vec) {
			topic[i] = vec[i]
		} else {
			topic[i] = "*"
		}
	}
	return topic
}

// topicVector reconstructs a full observed [name, params...] vector from a random emitter.
func (s *eventsSampler) topicVector() []string {
	td := s.events.ContractIds[s.emitterContract()]
	names, weights := td.TopicsAndWeights()
	vec := []string{chooseOne(s.rng, names, weights)}
	if params := td.Topic[vec[0]].Params; len(params) > 0 {
		vec = append(vec, params[s.rng.IntN(len(params))]...)
	}
	return vec
}

func (s *eventsSampler) coldContract() string {
	return chooseOne(s.rng, s.cold, s.coldWeights)
}

func (s *eventsSampler) emitterContract() string {
	return chooseOne(s.rng, s.emitters, s.emitterWeights)
}

// paramValue returns a real observed ScVal (base64) to use as a rare-but-real topic value.
func (s *eventsSampler) paramValue() string {
	if len(s.paramPool) == 0 {
		return s.transferSym
	}
	return s.paramPool[s.rng.IntN(len(s.paramPool))]
}

// setLimit sets a request's limit in the request body. the limit is chosen
// from a list of limits according to the provided weights.
func (s *eventsSampler) setLimit(body map[string]any, limits []uint, weights []float64) {
	body["pagination"] = map[string]any{"limit": chooseOne(s.rng, limits, weights)}
}

// placeDeep returns a start at least EventsDeepBandFloor behind head (or `around` deep
// when non-zero), clamped into the placeable window.
func (s *eventsSampler) placeDeep(around uint32) uint32 {
	retention := s.head.Latest - s.floorLedger()
	depth := util.EventsDeepBandFloor
	if around != 0 {
		depth = around + uint32(s.rng.IntN(5000))
	} else if retention > util.EventsDeepBandFloor {
		depth += uint32(s.rng.IntN(int(retention - util.EventsDeepBandFloor + 1)))
	}
	depth = min(depth, retention)
	return s.head.Latest - depth
}

// clampStart bounds a start into [oldest+margin, latest]; small retention windows
// degrade deep placements toward the floor rather than erroring.
func (s *eventsSampler) clampStart(start uint32) uint32 {
	return min(max(start, s.floorLedger()), s.head.Latest)
}

func (s *eventsSampler) floorLedger() uint32 {
	return min(s.head.Oldest+util.EventsLeftEdgeMargin, s.head.Latest)
}

// coldPoolFromKeys gets deployed-but-quiet contract IDs from seeded ledger keys.
func coldPoolFromKeys(keys []string, exclude []string, n int) []string {
	seen := make(map[string]bool, n)
	var out []string
	for _, k := range keys {
		var key xdr.LedgerKey
		if xdr.SafeUnmarshalBase64(k, &key) != nil || key.Type != xdr.LedgerEntryTypeContractData {
			continue
		}
		cid, ok := key.ContractData.Contract.GetContractId()
		if !ok {
			continue
		}
		id := strkey.MustEncode(strkey.VersionByteContract, cid[:])
		if seen[id] || slices.Contains(exclude, id) { // skip known emitters
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == n {
			break
		}
	}
	return out
}

func collectParams(events seed.ContractEvents) []string {
	var out []string
	for _, td := range events.ContractIds {
		for _, pt := range td.Topic {
			for _, params := range pt.Params {
				out = append(out, params...)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
