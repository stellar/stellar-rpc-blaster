package benchmark

import (
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"

	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type simulatedInvocationTemplate struct {
	invokeArgs xdr.InvokeContractArgs
	simulation sharedsoroban.SimulatedInvocation
}

type footprintKeyBuilder func() (xdr.LedgerKey, error)

func presimulateBenchmarkInvocation(
	st *state.State,
	txSourceKP *keypair.Full,
	opSourceAddress string,
	invokeArgs xdr.InvokeContractArgs,
) (simulatedInvocationTemplate, error) {
	sim, err := sharedsoroban.SimulatePaddedInvokeContract(
		st,
		txSourceKP,
		opSourceAddress,
		invokeArgs,
		benchmarkBaseFee,
		resourcePadFactor,
	)
	if err != nil {
		return simulatedInvocationTemplate{}, err
	}

	return simulatedInvocationTemplate{
		invokeArgs: invokeArgs,
		simulation: sim,
	}, nil
}

func buildFootprintFromTemplate(tmpl xdr.LedgerFootprint, builders ...footprintKeyBuilder) (xdr.LedgerFootprint, error) {
	readWrite := make([]xdr.LedgerKey, 0, len(builders))
	for _, builder := range builders {
		key, err := builder()
		if err != nil {
			return xdr.LedgerFootprint{}, err
		}
		readWrite = append(readWrite, key)
	}

	return sharedsoroban.ReplaceFootprintReadWriteKeys(tmpl, readWrite...), nil
}
