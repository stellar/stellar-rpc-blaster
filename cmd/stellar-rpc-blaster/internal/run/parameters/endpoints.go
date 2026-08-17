package parameters

import (
	"fmt"
	"math/rand/v2"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

// Builds a list of params maps for a data-dependent endpoint to vary request payloads.
func BuildEndpointParams(endpointKey string, maxNeededNumBodies int, params *Parameters, limit uint32) ([]map[string]any, error) {
	if needs, err := EndpointNeedsData(endpointKey); err != nil {
		return nil, err
	} else if !needs {
		return []map[string]any{{}}, nil
	}

	switch endpointKey {
	case "getTransaction":
		// Hash-stream model: mostly repolls of recently polled hashes, fresh draws
		// split between seeded (found) hashes and a small never-landing pool.
		rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
		hashes := params.Output.TxHashes
		neverLand := make([]string, util.TxRecentWindow)
		for i := range neverLand {
			neverLand[i] = randomTxHash(rng)
		}
		count := min(maxNeededNumBodies, util.MaxNumPrebuiltBodies)
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
		count := min(len(keys), maxNeededNumBodies, util.MaxNumPrebuiltBodies)
		result := make([]map[string]any, count)
		for i := range count {
			n := min(util.VaryKeyCount(), uint(len(keys)))
			// Pick n distinct random keys
			keys := util.ChooseNAtRandom(keys, int(n))
			result[i] = map[string]any{
				"keys":      keys,
				"xdrFormat": util.VaryFormat(),
			}
		}
		return result, nil

	case "getTransactions", "getLedgers":
		if params.Head.Latest == 0 {
			return nil, fmt.Errorf("%s bodies need a preflight-captured ledger head", endpointKey)
		}
		rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
		prNear := util.PrTxsNearHead
		if endpointKey == "getLedgers" {
			prNear = util.PrLedgersNearHead
		}
		count := min(maxNeededNumBodies, util.MaxNumPrebuiltBodies)
		result := make([]map[string]any, count)
		for i := range count {
			entry := map[string]any{"startLedger": headStart(rng, params.Head, prNear)}
			switch {
			case limit != 0:
				entry["pagination"] = map[string]any{"limit": limit}
			case endpointKey == "getTransactions": // essentially always the max
				entry["pagination"] = map[string]any{"limit": util.MaxTxPageLimit}
			default: // getLedgers limit mix; 0 = key omitted (server default of 50)
				if l := chooseOne(rng, []uint{1, 5, 20, 0}, []float64{0.4, 0.28, 0.07, 0.25}); l != 0 {
					entry["pagination"] = map[string]any{"limit": l}
				}
			}
			result[i] = entry
		}
		return result, nil

	case "getEvents":
		s, err := newEventsSampler(params, limit, rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
		if err != nil {
			return nil, fmt.Errorf("couldn't build %s sampler: %w", endpointKey, err)
		}
		count := min(maxNeededNumBodies, util.MaxNumPrebuiltBodies)
		result := make([]map[string]any, count)
		for i := range count {
			_, result[i] = s.sample()
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported endpoint %s", endpointKey)
	}
}

// headStart draws a startLedger within 1k of head prNear of the time, else uniformly
// across the deeper lag band, clamped into [oldest+margin, latest].
func headStart(rng *rand.Rand, head HeadInfo, prNear float64) uint32 {
	retention := head.Latest - head.Oldest
	depth := uint32(rng.IntN(1000))
	if rng.Float64() >= prNear && retention > 1000 {
		depth = 1000 + uint32(rng.IntN(int(retention-1000)))
	}
	depth = min(depth, retention)
	return max(head.Latest-depth, min(head.Oldest+util.EventsLeftEdgeMargin, head.Latest))
}

func randomTxHash(rng *rand.Rand) string {
	var b [32]byte
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return fmt.Sprintf("%x", b)
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
