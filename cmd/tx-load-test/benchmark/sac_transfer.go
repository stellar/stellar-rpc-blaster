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

func (sacTransferMode) VerifyReady(ctx context.Context, st *state.State) error {
	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holderAccounts) < 2 {
		return fmt.Errorf("need at least 2 SAC holder accounts, got %d", len(holderAccounts))
	}
	if len(st.AccountKPs) == 0 {
		return fmt.Errorf("need at least 1 participant account for SAC tx sources")
	}

	for i, sacStr := range st.SACs {
		if sacStr == "" {
			return fmt.Errorf("SAC[%d] contract ID is empty -- run setup first", i)
		}
		contractID, err := decodeContractID(sacStr)
		if err != nil {
			return fmt.Errorf("decode SAC[%d] contract ID: %w", i, err)
		}
		exists, err := contractInstanceExists(ctx, st.RPCClient, contractID)
		if err != nil {
			return fmt.Errorf("check SAC[%d] contract instance: %w", i, err)
		}
		if !exists {
			return fmt.Errorf("SAC[%d] contract %s is missing on-ledger -- rerun setup", i, sacStr)
		}
	}

	balances, err := fetchTrustlineBalances(ctx, st, holderAccounts)
	if err != nil {
		return fmt.Errorf("fetch SAC holder trustlines: %w", err)
	}

	missingCount := 0
	examples := make([]string, 0, 5)
	for _, kp := range holderAccounts {
		accountBalances := balances[kp.Address()]
		for _, asset := range st.Assets {
			balance, ok := accountBalances[asset.GetCode()]
			if ok && balance > 0 {
				continue
			}
			missingCount++
			if len(examples) < cap(examples) {
				reason := "missing trustline"
				if ok && balance == 0 {
					reason = "zero balance"
				}
				examples = append(examples, fmt.Sprintf("%s %s (%s)", kp.Address(), asset.GetCode(), reason))
			}
		}
	}
	if missingCount > 0 {
		return fmt.Errorf(
			"SAC benchmark state incomplete: %d holder trustlines/balances missing or zero; examples: %s -- rerun setup",
			missingCount, formatExamples(examples),
		)
	}

	return nil
}

func (sacTransferMode) NewTargeter(ctx context.Context, rpcURL string, st *state.State) (vegeta.Targeter, SequenceResetFunc, error) {
	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holderAccounts) < 2 {
		return nil, nil, fmt.Errorf("need at least 2 SAC holder accounts for benchmark, got %d", len(holderAccounts))
	}
	if len(st.AccountKPs) == 0 {
		return nil, nil, fmt.Errorf("need at least 1 participant account for SAC tx sources")
	}
	for i, sac := range st.SACs {
		if sac == "" {
			return nil, nil, fmt.Errorf("SAC[%d] contract ID is empty -- run setup first", i)
		}
	}

	txSourceCount := len(st.AccountKPs)
	holderCount := len(holderAccounts)

	// Decode SAC contract IDs from strkey C... to raw 32-byte arrays.
	var sacIDs [3]xdr.ContractId
	for i, sacStr := range st.SACs {
		raw, err := strkey.Decode(strkey.VersionByteContract, sacStr)
		if err != nil {
			return nil, nil, fmt.Errorf("decode SAC[%d]: %w", i, err)
		}
		copy(sacIDs[i][:], raw)
	}

	// Convert classic assets to XDR for per-request footprint construction.
	var assetsXDR [3]xdr.Asset
	for i, a := range st.Assets {
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
	// keys are substituted per request; all ReadOnly entries returned by the
	// simulator are kept as-is.
	var (
		simResources          xdr.SorobanResources
		simResourceFee        xdr.Int64
		simFootprintTemplates [3]xdr.LedgerFootprint
	)
	for i := range sacIDs {
		simTxSource := st.AccountKPs[0]
		if simTxSource.Address() == holderAccounts[0].Address() && len(st.AccountKPs) > 2 {
			for _, candidate := range st.AccountKPs[1:] {
				if candidate.Address() != holderAccounts[0].Address() && candidate.Address() != holderAccounts[1].Address() {
					simTxSource = candidate
					break
				}
			}
		}

		r, fee, tmpl, err := presimulate(
			st, sacIDs[i],
			simTxSource, holderAccounts[0], holderAccounts[1],
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
	seqBase, err := loadSeqNums(ctx, st, st.AccountKPs)
	if err != nil {
		return nil, nil, fmt.Errorf("load SAC tx-source sequence numbers: %w", err)
	}

	// Per-account sequence counters.  Each counter holds the last sequence
	// number assigned for that account (initialised to the on-ledger value).
	// The targeter atomically increments a counter to obtain the next
	// sequence; on a non-consuming failure the runner calls resetSeq to
	// decrement it back so the sequence is reused on the next attempt.
	seqCounters := make([]atomic.Int64, txSourceCount)
	for i, base := range seqBase {
		seqCounters[i].Store(base)
	}

	// resetSeq reverts the sequence counter for the account identified by
	// the JSON-RPC ID.  The ID is set to slot+1 in the targeter and
	// txSrcIdx = slot % txSourceCount, so we can recover the account index.
	resetSeq := SequenceResetFunc(func(jsonRPCId int64) {
		txSrcIdx := int((jsonRPCId - 1) % int64(txSourceCount))
		seqCounters[txSrcIdx].Add(-1)
	})

	networkPassphrase := st.NetworkPassphrase

	// slotCounter is a global round-robin position. Tx-source account index is
	// slot % txSourceCount; this guarantees each transaction source appears at
	// most once per txSourceCount consecutive requests, eliminating
	// within-ledger sequence collisions.
	var slotCounter int64

	return func(t *vegeta.Target) error {
		// Claim the next slot and derive the transaction source account from it.
		slot := atomic.AddInt64(&slotCounter, 1) - 1
		txSrcIdx := int(slot % int64(txSourceCount))
		seq := seqCounters[txSrcIdx].Add(1)

		// Pick a random SAC and a source/destination holder pair.
		sacIdx := rand.IntN(len(st.SACs))
		sacID := sacIDs[sacIdx]
		assetXDR := assetsXDR[sacIdx]

		holderSrcIdx := rand.IntN(holderCount)
		dstIdx := rand.IntN(holderCount - 1)
		if dstIdx >= holderSrcIdx {
			dstIdx++
		}
		txSourceKP := st.AccountKPs[txSrcIdx]
		holderSrcKP := holderAccounts[holderSrcIdx]
		dstKP := holderAccounts[dstIdx]

		// Parse AccountIds needed for ScAddress args and footprint keys.
		holderSrcAccID, err := xdr.AddressToAccountId(holderSrcKP.Address())
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
				Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &holderSrcAccID,
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

		footprint := buildFootprintFromTemplate(simFootprintTemplates[sacIdx], assetXDR, holderSrcAccID, dstAccID)

		op := txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &invokeArgs,
			},
			// SorobanCredentialsSourceAccount: the operation source account's
			// signature authorizes the contract invocation. When the transaction
			// source differs from the holder source, both accounts sign.
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
			SourceAccount: holderSrcKP.Address(),
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
				AccountID: txSourceKP.Address(),
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

		signers := []*keypair.Full{txSourceKP}
		if holderSrcKP.Address() != txSourceKP.Address() {
			signers = append(signers, holderSrcKP)
		}
		tx, err = tx.Sign(networkPassphrase, signers...)
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
// host reads and writes, including any additional entries the simulator deems
// necessary beyond the trustlines explicitly substituted later.
func presimulate(
	state *state.State,
	sacID xdr.ContractId,
	txSourceKP, srcKP, dstKP *keypair.Full,
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
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: txSourceKP.Address(), Sequence: 1},
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

// loadSeqNums fetches the current on-ledger sequence number for every account
// in accountKPs using up to 50 concurrent LoadAccount calls. The returned
// slice is the same length/order as accountKPs and is used as the per-account
// atomic counter seed in targeter closures.
func loadSeqNums(ctx context.Context, state *state.State, accountKPs []*keypair.Full) ([]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	n := len(accountKPs)
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

			acct, err := state.RPCClient.LoadAccount(ctx, accountKPs[i].Address())
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
