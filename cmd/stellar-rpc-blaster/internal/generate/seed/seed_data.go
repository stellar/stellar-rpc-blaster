package seed

import (
	"cmp"
	"maps"
	"slices"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

// SeedData is the unified struct for writing and reading seed data across run and generate
type SeedData struct {
	LedgerRange       Range          `json:"ledger_range"`
	TxHashes          []string       `json:"tx_hashes"`
	ContractEventData ContractEvents `json:"contract_events"`
	LedgerKeys        []string       `json:"ledger_keys"`
}

// ContractEvents maps contract IDs to their observed event data.
type ContractEvents struct {
	ContractIds map[string]*TopicData `json:"contract_ids"`
}

// TopicData maps a topic name to the parameters observed for that topic.
type TopicData struct {
	Topic map[string]*ParamTopics `json:"topic"`
	Count uint64                  `json:"count"` // total emissions observed for this contract
}

// Holds the parameters we observe for a given topic.
type ParamTopics struct {
	Params [][]string `json:"params"` // deduped observed param vectors, capped
	Count  uint64     `json:"count"`  // emissions observed for this topic
}

func (c *ContractEvents) AddEventData(eventData protocol.EventInfo) {
	if len(eventData.TopicXDR) == 0 {
		return
	}
	name, params := eventData.TopicXDR[0], eventData.TopicXDR[1:]

	td := c.ContractIds[eventData.ContractID]
	if td == nil {
		td = &TopicData{Topic: map[string]*ParamTopics{}}
		c.ContractIds[eventData.ContractID] = td
	}
	td.Count++

	pt := td.Topic[name]
	if pt == nil {
		if len(td.Topic) >= util.MaxSeedTopicsPerContract {
			return // cap reached, don't store new topics/params to avoid unbounded growth
		}
		pt = &ParamTopics{}
		td.Topic[name] = pt
	}
	pt.Count++

	// add the params if they're unique AND we're under the param cap
	if len(pt.Params) < util.MaxSeedParamSetsPerTopic &&
		!slices.ContainsFunc(pt.Params, func(p []string) bool { return slices.Equal(p, params) }) {
		pt.Params = append(pt.Params, params)
	}
}

// trim keeps only the top-n contracts by emission count.
func (c *ContractEvents) trim(n int) {
	if len(c.ContractIds) <= n {
		return
	}
	ids := slices.SortedFunc(maps.Keys(c.ContractIds), func(a, b string) int {
		// sort on emission count descending (then contract ID ascending if counts equal)
		return cmp.Or(cmp.Compare(c.ContractIds[b].Count, c.ContractIds[a].Count), cmp.Compare(a, b))
	})
	for _, id := range ids[n:] {
		delete(c.ContractIds, id)
	}
}

// keysAndWeights returns the map's keys with each entry's count as its weight.
func keysAndWeights[V any](m map[string]V, count func(V) uint64) ([]string, []float64) {
	keys := slices.Sorted(maps.Keys(m))
	weights := make([]float64, len(keys))
	for i, k := range keys {
		weights[i] = float64(count(m[k]))
	}
	return keys, weights
}

// ContractsAndWeights returns emitter contract IDs with emission counts as weights.
func (c ContractEvents) ContractsAndWeights() ([]string, []float64) {
	return keysAndWeights(c.ContractIds, func(t *TopicData) uint64 { return t.Count })
}

// TopicsAndWeights returns the contract's topic names with emission counts as weights.
func (t *TopicData) TopicsAndWeights() ([]string, []float64) {
	return keysAndWeights(t.Topic, func(p *ParamTopics) uint64 { return p.Count })
}
