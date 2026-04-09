package benchmark

import (
	"context"
	"fmt"
	"math/rand/v2"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type ozTransferMode struct{}

func (ozTransferMode) Label() string { return "oz-transfer" }

func (ozTransferMode) VerifyReady(ctx context.Context, st *state.State) error {
	if len(st.AccountKPs) < 2 {
		return benchmarkStateCountError("OZ", 2, len(st.AccountKPs), "participant account")
	}
	if st.OZTokenContract == "" {
		return benchmarkMissingContractIDError("OZ", "OZ token")
	}

	contractID, err := requireReadyContract(ctx, st, "OZ token", st.OZTokenContract)
	if err != nil {
		return err
	}

	balances, err := ledger.FetchOZBalances(ctx, st.RPCClient, contractID, st.AccountKPs, readinessBatchSize)
	if err != nil {
		return fmt.Errorf("fetch OZ balances: %w", err)
	}

	missingCount := 0
	examples := make([]string, 0, 5)
	for _, kp := range st.AccountKPs {
		balance, ok := balances[kp.Address()]
		if ok && ledger.HasPositiveI128(balance) {
			continue
		}
		missingCount++
		if len(examples) < cap(examples) {
			reason := "missing balance entry"
			if ok {
				reason = "zero balance"
			}
			examples = append(examples, fmt.Sprintf("%s (%s)", kp.Address(), reason))
		}
	}
	if missingCount > 0 {
		return fmt.Errorf(
			"OZ benchmark state incomplete: %d accounts missing positive OZ balances; examples: %s -- rerun setup",
			missingCount, formatExamples(examples),
		)
	}

	return nil
}

func (ozTransferMode) NewTargeter(ctx context.Context, rpcURL string, state *state.State, accounts accountLeaseManager) (vegeta.Targeter, error) {
	txSourceAccounts := accounts.Accounts(accountRequirementAnySource)
	if len(txSourceAccounts) < 2 {
		return nil, benchmarkTargeterCountError("OZ", 2, len(txSourceAccounts), "participant account")
	}
	if state.OZTokenContract == "" {
		return nil, benchmarkMissingContractIDError("OZ", "OZ token")
	}

	contractID, err := ledger.DecodeContractID(state.OZTokenContract)
	if err != nil {
		return nil, fmt.Errorf("decode OZ token contract ID: %w", err)
	}

	simTemplate, err := presimulateOZTransfer(state, contractID, txSourceAccounts[0], txSourceAccounts[1])
	if err != nil {
		return nil, fmt.Errorf("pre-simulate OZ transfer: %w", err)
	}

	n := len(txSourceAccounts)

	networkPassphrase := state.NetworkPassphrase

	return func(t *vegeta.Target) error {
		lease, err := accounts.Acquire(ctx, accountRequirementAnySource)
		if err != nil {
			return fmt.Errorf("lease OZ tx-source account: %w", err)
		}
		releaseLease := true
		defer func() {
			if releaseLease {
				accounts.ReleaseRetryable(lease.RequestID)
			}
		}()

		dstIdx := rand.IntN(n - 1)
		srcKP := lease.Account
		srcAddress := srcKP.Address()
		if txSourceAccounts[dstIdx].Address() == srcAddress {
			dstIdx++
			if dstIdx >= n {
				dstIdx = 0
			}
		}

		dstKP := txSourceAccounts[dstIdx]

		srcAccID, err := xdr.AddressToAccountId(srcKP.Address())
		if err != nil {
			return fmt.Errorf("parse src account: %w", err)
		}
		dstAccID, err := xdr.AddressToAccountId(dstKP.Address())
		if err != nil {
			return fmt.Errorf("parse dst account: %w", err)
		}

		invokeArgs := xdr.InvokeContractArgs{
			ContractAddress: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &contractID,
			},
			FunctionName: "transfer",
			Args: xdr.ScVec{
				{
					Type: xdr.ScValTypeScvAddress,
					Address: &xdr.ScAddress{
						Type:      xdr.ScAddressTypeScAddressTypeAccount,
						AccountId: &srcAccID,
					},
				},
				{
					Type: xdr.ScValTypeScvAddress,
					Address: &xdr.ScAddress{
						Type:      xdr.ScAddressTypeScAddressTypeAccount,
						AccountId: &dstAccID,
					},
				},
				{
					Type: xdr.ScValTypeScvI128,
					I128: &xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(sacTransferAmount)},
				},
			},
		}

		footprint, err := buildOZFootprintFromTemplate(simTemplate.simulation.Footprint, contractID, srcKP.Address(), dstKP.Address())
		if err != nil {
			return fmt.Errorf("build OZ footprint: %w", err)
		}

		body, err := buildSorobanSendTransactionBody(sorobanSendTransactionParams{
			RPCID:             lease.RequestID,
			NetworkPassphrase: networkPassphrase,
			TxSource:          srcKP,
			Sequence:          lease.Sequence,
			Signers:           []*keypair.Full{srcKP},
			OpSourceAccount:   srcKP.Address(),
			InvokeArgs:        invokeArgs,
			AuthEntries: []xdr.SorobanAuthorizationEntry{{
				Credentials: xdr.SorobanCredentials{Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount},
				RootInvocation: xdr.SorobanAuthorizedInvocation{
					Function: xdr.SorobanAuthorizedFunction{
						Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
						ContractFn: &invokeArgs,
					},
				},
			}},
			Resources: xdr.SorobanResources{
				Footprint:     footprint,
				Instructions:  simTemplate.simulation.Resources.Instructions,
				DiskReadBytes: simTemplate.simulation.Resources.DiskReadBytes,
				WriteBytes:    simTemplate.simulation.Resources.WriteBytes,
			},
			ResourceFee: simTemplate.simulation.ResourceFee,
		})
		if err != nil {
			return err
		}
		populateJSONRPCTarget(t, rpcURL, body, lease.RequestID)
		releaseLease = false
		return nil
	}, nil
}

func presimulateOZTransfer(
	state *state.State,
	contractID xdr.ContractId,
	srcKP, dstKP *keypair.Full,
) (simulatedInvocationTemplate, error) {
	srcAccID, err := xdr.AddressToAccountId(srcKP.Address())
	if err != nil {
		return simulatedInvocationTemplate{}, err
	}
	dstAccID, err := xdr.AddressToAccountId(dstKP.Address())
	if err != nil {
		return simulatedInvocationTemplate{}, err
	}

	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		},
		FunctionName: "transfer",
		Args: xdr.ScVec{
			{
				Type: xdr.ScValTypeScvAddress,
				Address: &xdr.ScAddress{
					Type:      xdr.ScAddressTypeScAddressTypeAccount,
					AccountId: &srcAccID,
				},
			},
			{
				Type: xdr.ScValTypeScvAddress,
				Address: &xdr.ScAddress{
					Type:      xdr.ScAddressTypeScAddressTypeAccount,
					AccountId: &dstAccID,
				},
			},
			{
				Type: xdr.ScValTypeScvI128,
				I128: &xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(sacTransferAmount)},
			},
		},
	}

	return presimulateBenchmarkInvocation(state, srcKP, srcKP.Address(), invokeArgs)
}

func buildOZFootprintFromTemplate(
	tmpl xdr.LedgerFootprint,
	contractID xdr.ContractId,
	srcAddress, dstAddress string,
) (xdr.LedgerFootprint, error) {
	return buildFootprintFromTemplate(
		tmpl,
		func() (xdr.LedgerKey, error) {
			srcKey, err := ledger.OZBalanceLedgerKey(contractID, srcAddress)
			if err != nil {
				return xdr.LedgerKey{}, fmt.Errorf("src balance key: %w", err)
			}
			return srcKey, nil
		},
		func() (xdr.LedgerKey, error) {
			dstKey, err := ledger.OZBalanceLedgerKey(contractID, dstAddress)
			if err != nil {
				return xdr.LedgerKey{}, fmt.Errorf("dst balance key: %w", err)
			}
			return dstKey, nil
		},
	)
}
