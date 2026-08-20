package parameters

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

// Builds a list of params maps for a data-dependent endpoint to vary request payloads.
// A non-zero limit is a config override applied to every body (config validation
// guarantees only paginated endpoints can carry one).
func BuildEndpointParams(endpointKey string, maxNeededNumBodies int, params *Parameters, limit uint32) ([]map[string]any, error) {
	result, err := buildEndpointParams(endpointKey, maxNeededNumBodies, params)
	if limit != 0 {
		for _, body := range result {
			body["pagination"] = map[string]any{"limit": limit}
		}
	}
	return result, err
}

func buildEndpointParams(endpointKey string, maxNeededNumBodies int, params *Parameters) ([]map[string]any, error) {
	if needs, err := EndpointNeedsData(endpointKey); err != nil {
		return nil, err
	} else if !needs {
		return []map[string]any{{}}, nil
	}
	rng := rand.New(rand.NewPCG(params.RngSeed, endpointStream(endpointKey)))
	count := min(maxNeededNumBodies, util.MaxNumPrebuiltBodies)

	switch endpointKey {
	case "getTransaction":
		// Hash-stream model: mostly repolls of recently polled hashes, fresh draws
		// split between seeded (found) hashes and a small never-landing pool.
		hashes := params.Output.TxHashes
		neverLand := make([]string, 8) // small: dead hashes get re-polled, like real never-landing targets
		for i := range neverLand {
			neverLand[i] = fmt.Sprintf("%016x%016x%016x%016x", rng.Uint64(), rng.Uint64(), rng.Uint64(), rng.Uint64())
		}
		result := make([]map[string]any, count)
		var recent []string
		for i := range count {
			var hash string
			if len(recent) > 0 && rng.Float64() < util.PrTxRepoll {
				hash = recent[rng.IntN(len(recent))]
			} else {
				hash = hashes[rng.IntN(len(hashes))]
				if rng.Float64() < util.PrTxNotFound {
					hash = neverLand[rng.IntN(len(neverLand))]
				}
				if recent = append(recent, hash); len(recent) > util.TxRecentWindow {
					recent = recent[1:]
				}
			}
			result[i] = map[string]any{"hash": hash}
		}
		return result, nil

	case "getLedgerEntries":
		keys := params.Output.LedgerKeys
		count := min(len(keys), count)
		result := make([]map[string]any, count)
		for i := range count {
			n := min(util.VaryKeyCount(rng), uint(len(keys)))
			// Pick n distinct random keys
			keys := util.ChooseNAtRandomSeeded(keys, int(n), rng)
			result[i] = map[string]any{
				"keys":      keys,
				"xdrFormat": util.VaryFormat(rng),
			}
		}
		return result, nil

	case "getTransactions", "getLedgers":
		if params.Head.Latest == 0 {
			return nil, fmt.Errorf("%s bodies need a preflight-captured ledger head", endpointKey)
		}
		prNear := util.PrTxsNearHead
		if endpointKey == "getLedgers" {
			prNear = util.PrLedgersNearHead
		}
		result := make([]map[string]any, count)
		for i := range count {
			entry := map[string]any{"startLedger": headStart(rng, params.Head, prNear)}
			if endpointKey == "getTransactions" { // essentially always the max
				entry["pagination"] = map[string]any{"limit": util.MaxTxPageLimit}
			} else if l := chooseOne(rng, []uint{1, 5, 20, 0}, []float64{0.4, 0.28, 0.07, 0.25}); l != 0 {
				entry["pagination"] = map[string]any{"limit": l} // getLedgers mix; 0 = key omitted (server default of 50)
			}
			result[i] = entry
		}
		return result, nil

	case "getEvents":
		s, err := newEventsSampler(params, rng)
		if err != nil {
			return nil, fmt.Errorf("couldn't build %s sampler: %w", endpointKey, err)
		}
		result := make([]map[string]any, count)
		for i := range count {
			result[i] = s.sample()
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported endpoint %s", endpointKey)
	}
}

// endpointStream derives a per-endpoint PCG stream from the endpoint name, so one root
// seed gives every endpoint an independent draw sequence. Keyed by name, not position,
// so enabling or reordering endpoints doesn't perturb the others.
func endpointStream(endpointKey string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(endpointKey)) // can't fail
	return h.Sum64()
}

// headStart draws a startLedger within 1k of head prNear of the time, else uniformly
// across the deeper lag band.
func headStart(rng *rand.Rand, head HeadInfo, prNear float64) uint32 {
	depth := uint32(rng.IntN(1000))
	if retention := head.Latest - head.Floor(); rng.Float64() >= prNear && retention > 1000 {
		depth = 1000 + uint32(rng.IntN(int(retention-1000)))
	}
	return head.Back(depth)
}

// EndpointNeedsData reports whether the endpoint requires seed data to build requests.
func EndpointNeedsData(endpointKey string) (bool, error) {
	switch endpointKey {
	case "getTransaction", "getLedgerEntries", "getTransactions", "getLedgers", "getEvents", "simulateTransaction":
		return true, nil
	case "getHealth", "getNetwork", "getVersionInfo", "getLatestLedger", "getFeeStats":
		return false, nil
	default:
		return false, fmt.Errorf("unknown endpoint %q", endpointKey)
	}
}

const trafficProfileVersion = 3 // bump when any endpoint's traffic model changes

// ProfileVersion returns the traffic-model version stamped into results for
// modeled endpoints (0 = endpoint has no model).
func ProfileVersion(endpointKey string) int {
	switch endpointKey {
	case "getEvents", "getTransaction", "getTransactions", "getLedgers":
		return trafficProfileVersion
	}
	return 0
}
