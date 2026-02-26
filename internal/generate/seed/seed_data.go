package seed

import (
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

// SeedData is the unified struct for writing and reading seed data across run and generate
type SeedData struct {
	LedgerRange util.Range `json:"ledger_range"`
	TxHashes    []string   `json:"tx_hashes"`
	ContractIDs []string   `json:"contract_ids"`
	EventTopics []string   `json:"event_topics"`
	LedgerKeys  []string   `json:"ledger_keys"`
}
