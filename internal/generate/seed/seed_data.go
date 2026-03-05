package seed

import (
	"maps"
	"math/rand/v2"
	"slices"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/collections/set"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// SeedData is the unified struct for writing and reading seed data across run and generate
type SeedData struct {
	LedgerRange       Range          `json:"ledger_range"`
	TxHashes          []string       `json:"tx_hashes"`
	ContractEventData ContractEvents `json:"contract_events"`
	LedgerKeys        []string       `json:"ledger_keys"`
}

// ContractEvents maps the contract IDs to their corresponding event data.
type ContractEvents struct {
	ContractIds map[string]*TopicData `json:"contract_ids"`
}

// TopicData maps a topic to the parameters observed for that topic.
type TopicData struct {
	Topic        map[string]ParamTopics `json:"topic"`
	uniqueTopics set.Set[string]        // set of unique topics observed for this contract, used to determine if a topic is new or existing for this contract
}

// Holds the parameters we observe for a given topic.
type ParamTopics struct {
	Params [][]string `json:"params"`
}

func (c *ContractEvents) AddEventData(contractId string, eventData protocol.EventInfo) {
	td, ok := c.ContractIds[contractId]
	if !ok {
		// new contract, add contract + topic + params
		data := TopicData{
			Topic:        map[string]ParamTopics{},
			uniqueTopics: set.NewSet[string](int(util.DefaultSeedSliceSize)),
		}
		c.ContractIds[contractId] = &data
		td = &data
	}

	topicXdr := eventData.TopicXDR
	name := topicXdr[0]    // first topic is the event name
	params := topicXdr[1:] // subsequent topics are the event parameters

	if td.uniqueTopics.Contains(name) {
		// contract + topic both exist
		topicParams := td.Topic[name].Params
		td.Topic[name] = ParamTopics{Params: append(topicParams, params)}
	} else {
		// contract exists, topic is new
		td.uniqueTopics.Add(name)
		td.Topic[name] = ParamTopics{Params: [][]string{params}}
	}
}

func (c *ContractEvents) BuildEventsFilter() map[string]any {
	var cIds []string
	filter := make(map[string]any)
	// Choose up to 5 random contract IDs
	nCIds := rand.IntN(5)
	cIds = util.ChooseNAtRandom(c.getContractIds(), max(nCIds, 1))
	if nCIds != 0 {
		// In the 0 case, even though we picked one contract ID for filtering purposes, add none
		filter["contractIds"] = cIds
	}
	if len(cIds) == 0 {
		return filter
	}

	refContractTopics := c.ContractIds[cIds[0]]
	// Choose up to 5 random topics from the first contract ID's topics
	nTopics := rand.IntN(5)
	alltopics, weights := refContractTopics.getTopicsAndWeights()
	topics := util.WeightedChooseN(alltopics, weights, max(nTopics, 1))

	// For each topic we chose, choose up to 4 parameters to include in that topic's filter
	topicsFilter := make([][]string, 0, len(topics))
	for _, topic := range topics {
		entry := []string{topic} // first entry in the topic filter is the topic name
		// choose some random parameters/invocation for this topic
		allParams := refContractTopics.Topic[topic].Params
		params := allParams[rand.IntN(len(allParams))]

		// choose up to 4 of the chosen invocation's parameters to include in the filter
		nParams := min(rand.IntN(4), len(params))
		entry = append(entry, params[:nParams]...)
		topicsFilter = append(topicsFilter, entry)
	}

	if nTopics != 0 {
		filter["topics"] = topicsFilter
	}
	return filter
}

func (c ContractEvents) getContractIds() []string {
	return slices.Collect(maps.Keys(c.ContractIds))
}

func (t TopicData) getTopicsAndWeights() ([]string, []int) {
	topics := make([]string, 0, len(t.Topic))
	weights := make([]int, 0, len(t.Topic))
	for topic, pt := range t.Topic {
		topics = append(topics, topic)
		weights = append(weights, len(pt.Params))
	}
	return topics, weights
}
