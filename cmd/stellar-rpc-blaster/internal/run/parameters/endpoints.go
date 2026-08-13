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
		count := min(len(params.Output.TxHashes), maxNeededNumBodies, util.MaxNumPrebuiltBodies)
		result := make([]map[string]any, count)
		for i, hash := range params.Output.TxHashes[:count] {
			result[i] = map[string]any{
				"hash":      hash,
				"xdrFormat": util.VaryFormat(),
			}
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
		lr := params.Output.LedgerRange
		span := int(lr.Last) - int(lr.First)
		if span <= 0 {
			return nil, fmt.Errorf("empty ledger range for %s", endpointKey)
		}
		count := min(span, maxNeededNumBodies, util.MaxNumPrebuiltBodies)

		result := make([]map[string]any, count)
		for i := range count {
			start := lr.First + uint32(rand.IntN(span))
			remaining := lr.Last - start
			entry := map[string]any{
				"startLedger": start,
				"pagination": map[string]any{
					"limit": min(limit, remaining),
				},
				"xdrFormat": util.VaryFormat(),
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
