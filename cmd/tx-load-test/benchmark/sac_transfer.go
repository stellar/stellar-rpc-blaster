package benchmark

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync/atomic"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
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
		if _, err := requireReadyContract(ctx, st, fmt.Sprintf("SAC[%d]", i), sacStr); err != nil {
			return err
		}
	}

	return verifyTrustlineBalancesReady(ctx, st, holderAccounts, "SAC")
}

func (sacTransferMode) NewTargeter(ctx context.Context, rpcURL string, st *state.State, txSourceAccounts []*keypair.Full) (vegeta.Targeter, SequenceResetFunc, error) {
	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holderAccounts) < 2 {
		return nil, nil, fmt.Errorf("need at least 2 SAC holder accounts for benchmark, got %d", len(holderAccounts))
	}
	if len(txSourceAccounts) == 0 {
		return nil, nil, fmt.Errorf("need at least 1 participant account for SAC tx sources")
	}
	for i, sac := range st.SACs {
		if sac == "" {
			return nil, nil, fmt.Errorf("SAC[%d] contract ID is empty -- run setup first", i)
		}
	}

	txSourceCount := len(txSourceAccounts)
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
		simTxSource := txSourceAccounts[0]
		if simTxSource.Address() == holderAccounts[0].Address() && len(txSourceAccounts) > 2 {
			for _, candidate := range txSourceAccounts[1:] {
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
	seqs, err := newSequenceManager(ctx, st, txSourceAccounts, "SAC tx-source")
	if err != nil {
		return nil, nil, err
	}

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
		seq := seqs.Next(txSrcIdx)

		// Pick a random SAC and a source/destination holder pair.
		sacIdx := rand.IntN(len(st.SACs))
		sacID := sacIDs[sacIdx]
		assetXDR := assetsXDR[sacIdx]

		holderSrcIdx := rand.IntN(holderCount)
		dstIdx := rand.IntN(holderCount - 1)
		if dstIdx >= holderSrcIdx {
			dstIdx++
		}
		txSourceKP := txSourceAccounts[txSrcIdx]
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

		signers := []*keypair.Full{txSourceKP}
		if holderSrcKP.Address() != txSourceKP.Address() {
			signers = append(signers, holderSrcKP)
		}

		id := slot + 1
		body, err := buildSorobanSendTransactionBody(sorobanSendTransactionParams{
			RPCID:             id,
			NetworkPassphrase: networkPassphrase,
			TxSource:          txSourceKP,
			Sequence:          seq,
			Signers:           signers,
			OpSourceAccount:   holderSrcKP.Address(),
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
				Instructions:  simResources.Instructions,
				DiskReadBytes: simResources.DiskReadBytes,
				WriteBytes:    simResources.WriteBytes,
			},
			ResourceFee: simResourceFee,
		})
		if err != nil {
			return err
		}
		populateJSONRPCTarget(t, rpcURL, body)
		return nil
	}, seqs.ResetFunc(), nil
}

// presimulate calls SimulateTransaction with the given src/dst keypair and
// returns the resource budget plus the complete footprint as computed by the
// simulator.
func presimulate(
	state *state.State,
	sacID xdr.ContractId,
	txSourceKP, srcKP, dstKP *keypair.Full,
) (xdr.SorobanResources, xdr.Int64, xdr.LedgerFootprint, error) {
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

	sim, err := sharedsoroban.SimulatePaddedInvokeContract(state, txSourceKP, srcKP.Address(), invokeArgs, benchmarkBaseFee, resourcePadFactor)
	if err != nil {
		return xdr.SorobanResources{}, 0, xdr.LedgerFootprint{}, err
	}
	return sim.Resources, sim.ResourceFee, sim.Footprint, nil
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
	return sharedsoroban.ReplaceFootprintReadWriteKeys(
		tmpl,
		xdr.LedgerKey{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: src, Asset: tla}},
		xdr.LedgerKey{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: dst, Asset: tla}},
	)
}
