package seed

import (
	"cmp"
	"maps"
	"slices"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/util"
)

const CurrentSeedVersion = 2 // bump when SeedData's schema changes incompatibly

// SeedData is the unified struct for writing and reading seed data across run and generate
type SeedData struct {
	Version           int            `json:"version"`
	LedgerRange       Range          `json:"ledger_range"`
	TxHashes          []string       `json:"tx_hashes"`
	ContractEventData ContractEvents `json:"contract_events"`
	LedgerKeys        []string       `json:"ledger_keys"`
}

// ContractEvents maps contract IDs to their observed event data. Stored payload is
// capped (top contracts, few topics, few deduped param vectors) while counts keep
// accumulating, so emission weights survive the caps without unbounded seed growth.
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
			return
		}
		pt = &ParamTopics{}
		td.Topic[name] = pt
	}
	pt.Count++

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
		return cmp.Or(cmp.Compare(c.ContractIds[b].Count, c.ContractIds[a].Count), cmp.Compare(a, b))
	})
	for _, id := range ids[n:] {
		delete(c.ContractIds, id)
	}
}

// ContractsAndWeights returns emitter contract IDs with their emission counts as
// weights, sorted for deterministic iteration.
func (c ContractEvents) ContractsAndWeights() ([]string, []int) {
	ids := slices.Sorted(maps.Keys(c.ContractIds))
	weights := make([]int, len(ids))
	for i, id := range ids {
		weights[i] = int(min(c.ContractIds[id].Count, 1<<31))
	}
	return ids, weights
}

// TopicsAndWeights returns the contract's topic names with their emission counts as weights.
func (t *TopicData) TopicsAndWeights() ([]string, []int) {
	names := slices.Sorted(maps.Keys(t.Topic))
	weights := make([]int, len(names))
	for i, name := range names {
		weights[i] = int(min(t.Topic[name].Count, 1<<31))
	}
	return names, weights
}
