package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

type ozTransferMode struct{}

func (ozTransferMode) Label() string { return "oz-transfer" }

func (ozTransferMode) NewTargeter(ctx context.Context, rpcURL string, state *state.State) (vegeta.Targeter, SequenceResetFunc, error) {
	if len(state.AccountKPs) < 2 {
		return nil, nil, fmt.Errorf("need at least 2 accounts for OZ transfer benchmark, got %d", len(state.AccountKPs))
	}
	if state.OZTokenContract == "" {
		return nil, nil, fmt.Errorf("OZ token contract ID is empty -- run setup first")
	}

	raw, err := strkey.Decode(strkey.VersionByteContract, state.OZTokenContract)
	if err != nil {
		return nil, nil, fmt.Errorf("decode OZ token contract ID: %w", err)
	}
	var contractID xdr.ContractId
	copy(contractID[:], raw)

	simResources, simResourceFee, simFootprintTemplate, err := presimulateOZTransfer(state, contractID, state.AccountKPs[0], state.AccountKPs[1])
	if err != nil {
		return nil, nil, fmt.Errorf("pre-simulate OZ transfer: %w", err)
	}
	simResources.Instructions = xdr.Uint32(float64(simResources.Instructions) * resourcePadFactor)
	simResources.DiskReadBytes = xdr.Uint32(float64(simResources.DiskReadBytes) * resourcePadFactor)
	simResources.WriteBytes = xdr.Uint32(float64(simResources.WriteBytes) * resourcePadFactor)
	simResourceFee = xdr.Int64(float64(simResourceFee) * resourcePadFactor)

	n := len(state.AccountKPs)
	seqBase, err := loadAllSeqNums(ctx, state)
	if err != nil {
		return nil, nil, fmt.Errorf("load participant sequence numbers: %w", err)
	}

	seqCounters := make([]atomic.Int64, n)
	for i, base := range seqBase {
		seqCounters[i].Store(base)
	}

	resetSeq := SequenceResetFunc(func(jsonRPCID int64) {
		srcIdx := int((jsonRPCID - 1) % int64(n))
		seqCounters[srcIdx].Add(-1)
	})

	networkPassphrase := state.NetworkPassphrase
	var slotCounter int64

	return func(t *vegeta.Target) error {
		slot := atomic.AddInt64(&slotCounter, 1) - 1
		srcIdx := int(slot % int64(n))
		seq := seqCounters[srcIdx].Add(1)

		dstIdx := rand.IntN(n - 1)
		if dstIdx >= srcIdx {
			dstIdx++
		}

		srcKP := state.AccountKPs[srcIdx]
		dstKP := state.AccountKPs[dstIdx]

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

		totalFee := benchmarkBaseFee + int64(simResourceFee)
		tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount: &txnbuild.SimpleAccount{
				AccountID: srcKP.Address(),
				Sequence:  seq,
			},
			IncrementSequenceNum: false,
			Operations:           []txnbuild.Operation{&op},
			BaseFee:              totalFee,
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
	}, resetSeq, nil
}

func presimulateOZTransfer(
	state *state.State,
	contractID xdr.ContractId,
	srcKP, dstKP *keypair.Full,
) (xdr.SorobanResources, xdr.Int64, xdr.LedgerFootprint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
		Ext:           xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: srcKP.Address(), Sequence: 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              benchmarkBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
	})
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("build simulation transaction: %w", err)
	}

	b64, err := tx.Base64()
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("marshal simulation transaction: %w", err)
	}

	simResp, err := state.RPCClient.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("simulate: %w", err)
	}
	if simResp.Error != "" {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("simulate: %s", simResp.Error)
	}

	var sorobanData xdr.SorobanTransactionData
	if err = xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("parse simulation result: %w", err)
	}

	return sorobanData.Resources, sorobanData.ResourceFee, sorobanData.Resources.Footprint, nil
}

func buildOZFootprintFromTemplate(
	tmpl xdr.LedgerFootprint,
	contractID xdr.ContractId,
	srcAddress, dstAddress string,
) (xdr.LedgerFootprint, error) {
	srcKey, err := ozBalanceLedgerKey(contractID, srcAddress)
	if err != nil {
		return xdr.LedgerFootprint{}, fmt.Errorf("src balance key: %w", err)
	}
	dstKey, err := ozBalanceLedgerKey(contractID, dstAddress)
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

func ozBalanceLedgerKey(contractID xdr.ContractId, accountAddress string) (xdr.LedgerKey, error) {
	accountID, err := xdr.AddressToAccountId(accountAddress)
	if err != nil {
		return xdr.LedgerKey{}, err
	}

	balanceVariant := xdr.ScSymbol("Balance")
	balanceKeyVec := xdr.ScVec{
		{Type: xdr.ScValTypeScvSymbol, Sym: &balanceVariant},
		{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &accountID,
		}},
	}
	balanceKeyRef := &balanceKeyVec

	return xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &contractID,
			},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &balanceKeyRef},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}, nil
}
