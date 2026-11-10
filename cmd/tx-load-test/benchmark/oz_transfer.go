package benchmark

import (
	"bytes"
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
	// Capture the representative src/dst keys so the per-request rewrite can
	// drop them from the template's RW before appending the actual leased
	// trader balance keys. Without this, every OZ tx would carry the
	// representative src/dst entries plus the actual ones -- a duplicate-key
	// rejection at submit and a stale-index autorestore signal at apply.
	repSrcBalanceKey, err := ledger.OZBalanceLedgerKey(contractID, txSourceAccounts[0].Address())
	if err != nil {
		return nil, fmt.Errorf("encode OZ rep src balance key: %w", err)
	}
	repDstBalanceKey, err := ledger.OZBalanceLedgerKey(contractID, txSourceAccounts[1].Address())
	if err != nil {
		return nil, fmt.Errorf("encode OZ rep dst balance key: %w", err)
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

		invokeArgs := buildOZTransferInvokeArgs(contractID, srcAccID, dstAccID)

		footprint, dataExt, err := buildOZFootprintFromTemplate(
			simTemplate.simulation.Footprint,
			simTemplate.simulation.Ext,
			contractID,
			srcKP.Address(), dstKP.Address(),
			repSrcBalanceKey, repDstBalanceKey,
		)
		if err != nil {
			return fmt.Errorf("build OZ footprint: %w", err)
		}

		body, err := buildSorobanSendTransactionBody(sorobanSendTransactionParams{
			RPCID:             lease.RequestID,
			NetworkPassphrase: networkPassphrase,
			FeePayerKP:        state.FeePayerKP,
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
			// dataExt carries the remapped archivedSorobanEntries. The
			// representative src/dst balance entries are filtered out before
			// the new actual ones are appended, so any surviving simulator
			// indices (e.g. the OZ contract instance when archived) are
			// translated to their new positions and re-sorted.
			SorobanDataExt: dataExt,
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

	invokeArgs := buildOZTransferInvokeArgs(contractID, srcAccID, dstAccID)

	return presimulateBenchmarkInvocation(state, srcKP, srcKP.Address(), invokeArgs)
}

func buildOZTransferInvokeArgs(contractID xdr.ContractId, srcAccID, dstAccID xdr.AccountId) xdr.InvokeContractArgs {
	return xdr.InvokeContractArgs{
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
}

// buildOZFootprintFromTemplate substitutes the per-request src/dst balance
// keys for the representative ones the simulator saw. Non-balance template
// RW entries (e.g. the OZ contract instance) are kept in place; the actual
// src/dst balance entries are appended.
//
// Autorestore correctness hinges on a subtlety unique to OZ: unlike SAC and
// soroswap -- whose substituted entries are classic trustlines that never
// archive -- OZ's substituted entries ARE the per-account balance contract-data
// entries, which DO archive. So when the simulator marks the representative
// src/dst balances as archived, that marker must be INHERITED by the appended
// actual src/dst balances; otherwise apply reads an archived balance that isn't
// in archivedSorobanEntries and fails with invokeHostFunctionEntryArchived.
// Kept entries (the instance) remap their indices as usual.
//
// This inherits the rep balances' archival state onto whichever accounts this
// request happens to use. That is exact when balances age uniformly (all minted
// together at setup, identical TTL) -- the same uniformity the template approach
// already assumes. A per-request simulation would be exact in all cases but
// defeats the template optimization.
func buildOZFootprintFromTemplate(
	tmpl xdr.LedgerFootprint,
	tmplExt xdr.SorobanTransactionDataExt,
	contractID xdr.ContractId,
	srcAddress, dstAddress string,
	repSrcKey, repDstKey xdr.LedgerKey,
) (xdr.LedgerFootprint, xdr.SorobanTransactionDataExt, error) {
	srcKey, err := ledger.OZBalanceLedgerKey(contractID, srcAddress)
	if err != nil {
		return xdr.LedgerFootprint{}, xdr.SorobanTransactionDataExt{}, fmt.Errorf("src balance key: %w", err)
	}
	dstKey, err := ledger.OZBalanceLedgerKey(contractID, dstAddress)
	if err != nil {
		return xdr.LedgerFootprint{}, xdr.SorobanTransactionDataExt{}, fmt.Errorf("dst balance key: %w", err)
	}
	repSrcBytes, err := repSrcKey.MarshalBinary()
	if err != nil {
		return xdr.LedgerFootprint{}, xdr.SorobanTransactionDataExt{}, fmt.Errorf("marshal rep src key: %w", err)
	}
	repDstBytes, err := repDstKey.MarshalBinary()
	if err != nil {
		return xdr.LedgerFootprint{}, xdr.SorobanTransactionDataExt{}, fmt.Errorf("marshal rep dst key: %w", err)
	}

	archivedTemplate := archivedTemplateIndexSet(tmplExt)
	footprint := xdr.LedgerFootprint{
		ReadOnly:  append([]xdr.LedgerKey(nil), tmpl.ReadOnly...),
		ReadWrite: make([]xdr.LedgerKey, 0, len(tmpl.ReadWrite)+2),
	}
	var archived []xdr.Uint32
	repSrcArchived, repDstArchived := false, false
	for i, key := range tmpl.ReadWrite {
		keyBytes, err := key.MarshalBinary()
		if err != nil {
			return xdr.LedgerFootprint{}, xdr.SorobanTransactionDataExt{}, fmt.Errorf("marshal RW[%d]: %w", i, err)
		}
		// The rep src/dst balances are dropped here and re-appended below as the
		// actual accounts' keys; their archival state is carried onto the
		// appended entries rather than the (now-absent) template positions.
		if bytes.Equal(keyBytes, repSrcBytes) {
			repSrcArchived = repSrcArchived || archivedTemplate[i]
			continue
		}
		if bytes.Equal(keyBytes, repDstBytes) {
			repDstArchived = repDstArchived || archivedTemplate[i]
			continue
		}
		newIdx := len(footprint.ReadWrite)
		footprint.ReadWrite = append(footprint.ReadWrite, key)
		if archivedTemplate[i] {
			archived = append(archived, xdr.Uint32(newIdx))
		}
	}

	srcIdx := len(footprint.ReadWrite)
	footprint.ReadWrite = append(footprint.ReadWrite, srcKey)
	if repSrcArchived {
		archived = append(archived, xdr.Uint32(srcIdx))
	}
	dstIdx := len(footprint.ReadWrite)
	footprint.ReadWrite = append(footprint.ReadWrite, dstKey)
	if repDstArchived {
		archived = append(archived, xdr.Uint32(dstIdx))
	}

	return footprint, buildArchivedSorobanExt(archived), nil
}

// archivedTemplateIndexSet returns the set of read-write footprint indices the
// simulator marked for auto-restoration, for O(1) membership tests during a
// footprint rewrite.
func archivedTemplateIndexSet(ext xdr.SorobanTransactionDataExt) map[int]bool {
	if ext.V != 1 || ext.ResourceExt == nil {
		return nil
	}
	set := make(map[int]bool, len(ext.ResourceExt.ArchivedSorobanEntries))
	for _, idx := range ext.ResourceExt.ArchivedSorobanEntries {
		set[int(idx)] = true
	}
	return set
}
