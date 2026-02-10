package seed

import "github.com/pkg/errors"

type PreseedParameters struct {
	ExportPath string
	Range      Range `json:"ledger_range"`
}

type Range struct {
	First uint32 `json:"first"`
	Last  uint32 `json:"last"`
}

func WriteLedgerRangeEntry(params PreseedParameters, writer *SeedWriter) error {
	if err := writer.StartArray("ledger_range"); err != nil {
		return errors.Wrap(err, "failed to write ledger range start")
	}
	if err := writer.WriteItem(params.Range); err != nil {
		return errors.Wrap(err, "failed to write ledger range item")
	}
	if err := writer.EndArray(); err != nil {
		return errors.Wrap(err, "failed to write ledger range end")
	}
	return nil
}
