package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
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

// benchmarkBaseFee is the inclusion fee per operation (in stroops) applied to
// every benchmark transaction, on top of the Soroban resource fee.
const benchmarkBaseFee int64 = 200

// sacTransferAmount is 1.0 units of the asset in its 7-decimal raw form.
const sacTransferAmount = int64(10_000_000)

// resourcePadFactor is a negligible safety margin on the simulation-derived
// resource limits. Since all SAC transfer invocations have identical resource
// requirements (same Wasm path, fixed-size trustline entries), padding is only
// needed to absorb any minor ledger-growth between presimulation and the run.
const resourcePadFactor = 1.05

// rpcJSONBody is the JSON-RPC request envelope sent to the Stellar RPC server.
type rpcJSONBody struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      int64             `json:"id"`
	Method  string            `json:"method"`
	Params  map[string]string `json:"params"`
}

type sacTransferMode struct{}

func (sacTransferMode) Label() string { return "sac-transfer" }

func (sacTransferMode) NewTargeter(ctx context.Context, rpcURL string, state *state.State) (vegeta.Targeter, SequenceResetFunc, error) {
	if len(state.AccountKPs) < 2 {
		return nil, nil, fmt.Errorf("need at least 2 accounts for SAC transfer benchmark, got %d", len(state.AccountKPs))
	}
	for i, sac := range state.SACs {
		if sac == "" {
			return nil, nil, fmt.Errorf("SAC[%d] contract ID is empty -- run setup first", i)
		}
	}

	n := len(state.AccountKPs)

	// Decode SAC contract IDs from strkey C... to raw 32-byte arrays.
	var sacIDs [3]xdr.ContractId
	for i, sacStr := range state.SACs {
		raw, err := strkey.Decode(strkey.VersionByteContract, sacStr)
		if err != nil {
			return nil, nil, fmt.Errorf("decode SAC[%d]: %w", i, err)
		}
		copy(sacIDs[i][:], raw)
	}

	// Convert classic assets to XDR for per-request footprint construction.
	var assetsXDR [3]xdr.Asset
	for i, a := range state.Assets {
		ax, err := a.ToXDR()
		if err != nil {
			return nil, nil, fmt.Errorf("asset[%d] to XDR: %w", i, err)
		}
		assetsXDR[i] = ax
	}

	// Pre-simulate one transfer per SAC to obtain the exact resource budget
	// and  -- crucially  -- the authoritative footprint from the simulator.
	// Each SAC has a distinct contract instance key, so a single template
	// cannot cover all three; using the wrong instance produces the error
	// "trying to access contract instance outside of the footprint".
	// The per-SAC footprint is used as a template: only the two trustline
	// keys are substituted per request; all ReadOnly entries (contract
	// instance, issuer account, source account for auth) are kept as-is.
	var (
		simResources          xdr.SorobanResources
		simResourceFee        xdr.Int64
		simFootprintTemplates [3]xdr.LedgerFootprint
	)
	for i := range sacIDs {
		r, fee, tmpl, err := presimulate(
			state, sacIDs[i],
			state.AccountKPs[0], state.AccountKPs[1],
		)
		if err != nil {
			return nil, nil, fmt.Errorf("pre-simulate SAC[%d] transfer: %w", i, err)
		}
		simFootprintTemplates[i] = tmpl
		// All three SACs share the same WASM and logic; use the last
		// simulation's resource numbers (they should be identical).
		simResources = r
		simResourceFee = fee
	}
	simResources.Instructions = xdr.Uint32(float64(simResources.Instructions) * resourcePadFactor)
	simResources.DiskReadBytes = xdr.Uint32(float64(simResources.DiskReadBytes) * resourcePadFactor)
	simResources.WriteBytes = xdr.Uint32(float64(simResources.WriteBytes) * resourcePadFactor)
	simResourceFee = xdr.Int64(float64(simResourceFee) * resourcePadFactor)

	// Pre-load the on-ledger sequence numbers for every participant account.
	// These serve as the base for per-account atomic counters that track
	// sequence progress independently.
	seqBase, err := loadAllSeqNums(ctx, state)
	if err != nil {
		return nil, nil, fmt.Errorf("load participant sequence numbers: %w", err)
	}

	// Per-account sequence counters.  Each counter holds the last sequence
	// number assigned for that account (initialised to the on-ledger value).
	// The targeter atomically increments a counter to obtain the next
	// sequence; on a non-consuming failure the runner calls resetSeq to
	// decrement it back so the sequence is reused on the next attempt.
	seqCounters := make([]atomic.Int64, n)
	for i, base := range seqBase {
		seqCounters[i].Store(base)
	}

	// resetSeq reverts the sequence counter for the account identified by
	// the JSON-RPC ID.  The ID is set to slot+1 in the targeter and
	// srcIdx = slot % n, so we can recover the account index.
	resetSeq := SequenceResetFunc(func(jsonRPCId int64) {
		srcIdx := int((jsonRPCId - 1) % int64(n))
		seqCounters[srcIdx].Add(-1)
	})

	networkPassphrase := state.NetworkPassphrase

	// slotCounter is a global round-robin position.  Account index is
	// slot % n; this guarantees each account appears at most once per n
	// consecutive requests, eliminating within-ledger sequence collisions.
	var slotCounter int64

	return func(t *vegeta.Target) error {
		// Claim the next slot and derive the source account from it.
		slot := atomic.AddInt64(&slotCounter, 1) - 1
		srcIdx := int(slot % int64(n))
		seq := seqCounters[srcIdx].Add(1)

		// Pick a random SAC and a destination account distinct from src.
		sacIdx := rand.IntN(len(state.SACs))
		sacID := sacIDs[sacIdx]
		assetXDR := assetsXDR[sacIdx]

		dstIdx := rand.IntN(n - 1)
		if dstIdx >= srcIdx {
			dstIdx++
		}
		srcKP := state.AccountKPs[srcIdx]
		dstKP := state.AccountKPs[dstIdx]

		// Parse AccountIds needed for ScAddress args and footprint keys.
		srcAccID, err := xdr.AddressToAccountId(srcKP.Address())
		if err != nil {
			return fmt.Errorf("parse src account: %w", err)
		}
		dstAccID, err := xdr.AddressToAccountId(dstKP.Address())
		if err != nil {
			return fmt.Errorf("parse dst account: %w", err)
		}

		// Build transfer(src, dst, amount) invocation arguments.
		args := xdr.ScVec{
			{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
				Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &srcAccID,
			}},
			{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
				Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &dstAccID,
			}},
			{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{
				Hi: 0, Lo: xdr.Uint64(sacTransferAmount),
			}},
		}
		invokeArgs := xdr.InvokeContractArgs{
			ContractAddress: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &sacID,
			},
			FunctionName: "transfer",
			Args:         args,
		}

		footprint := buildFootprintFromTemplate(simFootprintTemplates[sacIdx], assetXDR, srcAccID, dstAccID)

		op := txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &invokeArgs,
			},
			// SorobanCredentialsSourceAccount: the tx-level signature from srcKP
			// serves as the Soroban authorization; no extra signing step needed.
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

		// The tx Fee field must cover inclusion fee + Soroban resource fee.
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

// presimulate calls SimulateTransaction with the given src/dst keypair
// and returns the resource budget plus the complete footprint as computed by
// the simulator.  The footprint is the authoritative set of ledger entries the
// host reads and writes, including entries (e.g. the source account for auth)
// that a manually constructed footprint might miss.
func presimulate(
	state *state.State,
	sacID xdr.ContractId,
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

	args := xdr.ScVec{
		{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &srcAccID,
		}},
		{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &dstAccID,
		}},
		{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{
			Hi: 0, Lo: xdr.Uint64(sacTransferAmount),
		}},
	}
	invokeArgs := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &sacID,
		},
		FunctionName: "transfer",
		Args:         args,
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
		// Empty SorobanTransactionData lets the simulator compute the footprint.
		Ext: xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: srcKP.Address(), Sequence: 1},
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{&op},
		BaseFee:              benchmarkBaseFee,
		// This tx is only used for simulateTransaction, never submitted; a
		// short window is fine and avoids leaving infinite-ttl envelopes around.
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
	})
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("build simulation transaction: %w", err)
	}

	b64, err := tx.Base64()
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, fmt.Errorf("marshal simulation transaction: %w", err)
	}

	simResp, err := state.RPCClient.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{
		Transaction: b64,
	})
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

// loadAllSeqNums fetches the current on-ledger sequence number for every
// participant account using up to 50 concurrent LoadAccount calls.  The
// returned slice is the same length as state.AccountKPs and is used as the
// per-account atomic counter seed in the targeter closure.
func loadAllSeqNums(ctx context.Context, state *state.State) ([]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	n := len(state.AccountKPs)
	seqNums := make([]int64, n)
	errs := make([]error, n)

	const concurrency = 50
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			acct, err := state.RPCClient.LoadAccount(ctx, state.AccountKPs[i].Address())
			if err != nil {
				errs[i] = err
				return
			}
			seq, err := acct.GetSequenceNumber()
			if err != nil {
				errs[i] = err
				return
			}
			seqNums[i] = seq
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("account[%d] load sequence: %w", i, err)
		}
	}
	return seqNums, nil
}

// buildFootprintFromTemplate takes the footprint returned by the simulator for
// a representative transfer (accounts[0] -> accounts[1]) and substitutes the two
// ReadWrite trustline keys with the actual src/dst accounts for this request.
// All ReadOnly entries (contract instance, issuer account, source account read
// for auth) are kept as-is since they are identical for every invocation.
func buildFootprintFromTemplate(
	tmpl xdr.LedgerFootprint,
	assetXDR xdr.Asset,
	src, dst xdr.AccountId,
) xdr.LedgerFootprint {
	tla := xdr.TrustLineAsset{
		Type:       assetXDR.Type,
		AlphaNum4:  assetXDR.AlphaNum4,
		AlphaNum12: assetXDR.AlphaNum12,
	}
	return xdr.LedgerFootprint{
		ReadOnly: tmpl.ReadOnly,
		ReadWrite: []xdr.LedgerKey{
			{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: src, Asset: tla}},
			{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: dst, Asset: tla}},
		},
	}
}
