package seed

import (
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

/*
for _, event := range eventsResponse.Events {
	cid := event.ContractID
	for _, topic := range event.TopicXDR {
		s.contractEvents.AddEventData(cid, topic, event.Params)
	}
}
*/

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
