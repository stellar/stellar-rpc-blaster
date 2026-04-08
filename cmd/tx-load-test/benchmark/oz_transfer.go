package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync/atomic"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type ozTransferMode struct{}

func (ozTransferMode) Label() string { return "oz-transfer" }

func (ozTransferMode) VerifyReady(ctx context.Context, st *state.State) error {
	if len(st.AccountKPs) < 2 {
		return fmt.Errorf("need at least 2 accounts for OZ transfer benchmark, got %d", len(st.AccountKPs))
	}
	if st.OZTokenContract == "" {
		return fmt.Errorf("OZ token contract ID is empty -- run setup first")
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

func (ozTransferMode) NewTargeter(ctx context.Context, rpcURL string, state *state.State, txSourceAccounts []*keypair.Full) (vegeta.Targeter, SequenceResetFunc, error) {
	if len(txSourceAccounts) < 2 {
		return nil, nil, fmt.Errorf("need at least 2 accounts for OZ transfer benchmark, got %d", len(txSourceAccounts))
	}
	if state.OZTokenContract == "" {
		return nil, nil, fmt.Errorf("OZ token contract ID is empty -- run setup first")
	}

	contractID, err := ledger.DecodeContractID(state.OZTokenContract)
	if err != nil {
		return nil, nil, fmt.Errorf("decode OZ token contract ID: %w", err)
	}

	simResources, simResourceFee, simFootprintTemplate, err := presimulateOZTransfer(state, contractID, txSourceAccounts[0], txSourceAccounts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("pre-simulate OZ transfer: %w", err)
	}

	n := len(txSourceAccounts)
	seqs, err := newSequenceManager(ctx, state, txSourceAccounts, "participant")
	if err != nil {
		return nil, nil, err
	}

	networkPassphrase := state.NetworkPassphrase
	var slotCounter int64

	return func(t *vegeta.Target) error {
		slot := atomic.AddInt64(&slotCounter, 1) - 1
		srcIdx := int(slot % int64(n))
		seq := seqs.Next(srcIdx)

		dstIdx := rand.IntN(n - 1)
		if dstIdx >= srcIdx {
			dstIdx++
		}

		srcKP := txSourceAccounts[srcIdx]
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

		footprint, err := buildOZFootprintFromTemplate(simFootprintTemplate, contractID, srcKP.Address(), dstKP.Address())
		if err != nil {
			return fmt.Errorf("build OZ footprint: %w", err)
		}

		op := txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &invokeArgs,
			},
			Auth: []xdr.SorobanAuthorizationEntry{{
				Credentials: xdr.SorobanCredentials{
					Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
				},
				RootInvocation: xdr.SorobanAuthorizedInvocation{
					Function: xdr.SorobanAuthorizedFunction{
						Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
						ContractFn: &invokeArgs,
					},
				},
			}},
			SourceAccount: srcKP.Address(),
			Ext: xdr.TransactionExt{
				V: 1,
				SorobanData: &xdr.SorobanTransactionData{
					Resources: xdr.SorobanResources{
						Footprint:     footprint,
						Instructions:  simResources.Instructions,
						DiskReadBytes: simResources.DiskReadBytes,
						WriteBytes:    simResources.WriteBytes,
					},
					ResourceFee: simResourceFee,
				},
			},
		}

		tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount: &txnbuild.SimpleAccount{
				AccountID: srcKP.Address(),
				Sequence:  seq,
			},
			IncrementSequenceNum: false,
			Operations:           []txnbuild.Operation{&op},
			BaseFee:              benchmarkBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
		})
		if err != nil {
			return fmt.Errorf("build transaction: %w", err)
		}

		tx, err = tx.Sign(networkPassphrase, srcKP)
		if err != nil {
			return fmt.Errorf("sign transaction: %w", err)
		}

		b64, err := tx.Base64()
		if err != nil {
			return fmt.Errorf("marshal transaction: %w", err)
		}

		id := slot + 1
		body, err := json.Marshal(rpcJSONBody{
			JSONRPC: "2.0",
			ID:      id,
			Method:  protocol.SendTransactionMethodName,
			Params:  map[string]string{"transaction": b64},
		})
		if err != nil {
			return fmt.Errorf("marshal JSON-RPC body: %w", err)
		}

		t.Method = http.MethodPost
		t.URL = rpcURL
		t.Body = body
		t.Header = http.Header{"Content-Type": {"application/json"}}
		return nil
	}, seqs.ResetFunc(), nil
}

func presimulateOZTransfer(
	state *state.State,
	contractID xdr.ContractId,
	srcKP, dstKP *keypair.Full,
) (xdr.SorobanResources, xdr.Int64, xdr.LedgerFootprint, error) {
	srcAccID, err := xdr.AddressToAccountId(srcKP.Address())
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, err
	}
	dstAccID, err := xdr.AddressToAccountId(dstKP.Address())
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, err
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

	return simulatePaddedInvokeContract(state, srcKP, srcKP.Address(), invokeArgs)
}

func buildOZFootprintFromTemplate(
	tmpl xdr.LedgerFootprint,
	contractID xdr.ContractId,
	srcAddress, dstAddress string,
) (xdr.LedgerFootprint, error) {
	srcKey, err := ledger.OZBalanceLedgerKey(contractID, srcAddress)
	if err != nil {
		return xdr.LedgerFootprint{}, fmt.Errorf("src balance key: %w", err)
	}
	dstKey, err := ledger.OZBalanceLedgerKey(contractID, dstAddress)
	if err != nil {
		return xdr.LedgerFootprint{}, fmt.Errorf("dst balance key: %w", err)
	}

	return xdr.LedgerFootprint{
		ReadOnly: tmpl.ReadOnly,
		ReadWrite: []xdr.LedgerKey{
			srcKey,
			dstKey,
		},
	}, nil
}
