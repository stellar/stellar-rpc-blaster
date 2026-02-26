package seed

// SeedData is the unified struct for writing and reading seed data across run and generate
type SeedData struct {
	LedgerRange Range    `json:"ledger_range"`
	TxHashes    []string `json:"tx_hashes"`
	ContractIDs []string `json:"contract_ids"`
	EventTopics []string `json:"event_topics"`
	LedgerKeys  []string `json:"ledger_keys"`
}
