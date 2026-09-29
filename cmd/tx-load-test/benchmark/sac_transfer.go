package benchmark

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// benchmarkBaseFeeMin and benchmarkBaseFeeMax bound the per-op inclusion fee
// (in stroops) randomly chosen for each submitted benchmark transaction.
//
// The [200, 500] range is deliberately LOW -- just above the 100-stroop
// network minimum. The bench's goal is to fill the *remaining* space in each
// ledger, not to crowd out real users: when organic traffic bids more than a
// few hundred stroops/op during genuine congestion, our transactions are
// outbid and excluded, which is the intended behavior. We are not trying to
// win the auction, only to occupy whatever capacity organic demand leaves
// behind. (A previous [10_000, 100_000] range aimed to clear self-induced
// surge floors and stay above external p95s; that over-bids for a
// fill-the-gap workload and inflates cost on pubnet.)
//
// We still sample per-tx rather than using a single fixed value, because
// Stellar core's SurgePricingUtils::computeBetterFee requires a STRICTLY
// greater per-op fee for a new tx to evict (or beat in per-ledger inclusion
// ranking) one already in the queue. With identical bids, every tx after the
// first ties and is rejected with txINSUFFICIENT_FEE -- observed at 17,666 of
// 18,000 OZ-transfer submits in a 5-min bench. Per-tx random sampling breaks
// those ties (collision rate ~= queue_depth^2 / (2*range_size)). This is the
// same primitive stellar-core's own LoadGenerator (TxGenerator::generateFee)
// uses.
//
// Trade-off of the narrower range: only 301 distinct buckets (vs 90,001
// before), so at ~800 tx/ledger our own submissions collide far more often.
// That matters only when the ledger is FULL and eviction requires a strictly
// greater bid -- i.e. exactly when we intend to yield to organic traffic
// anyway. Below saturation there is free space, txns are admitted directly,
// and ties are harmless. If a target network needs a different band (e.g. to
// sit just under a specific organic surge floor), re-tune based on observed
// getFeeStats inclusion fees.
const (
	benchmarkBaseFeeMin int64 = 200
	benchmarkBaseFeeMax int64 = 500
)

// sampleBenchmarkBaseFee returns a per-op inclusion fee uniformly sampled from
// [benchmarkBaseFeeMin, benchmarkBaseFeeMax]. Call once per submitted tx so
// each carries a distinct per-op bid; this is what allows successive
// submissions to avoid the SurgePricingUtils::computeBetterFee tie rule and
// either be admitted directly (queue not full) or evict a strictly cheaper
// occupant.
func sampleBenchmarkBaseFee() int64 {
	span := benchmarkBaseFeeMax - benchmarkBaseFeeMin + 1
	return benchmarkBaseFeeMin + rand.Int64N(span)
}

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
		return benchmarkStateCountError("SAC", 2, len(holderAccounts), "trustlined holder account")
	}
	if len(st.AccountKPs) == 0 {
		return benchmarkStateCountError("SAC", 1, len(st.AccountKPs), "participant account")
	}

	for i, sacStr := range st.SACs {
		if _, err := requireReadyContract(ctx, st, fmt.Sprintf("SAC[%d]", i), sacStr); err != nil {
			return err
		}
	}

	return verifyTrustlineBalancesReady(ctx, st, holderAccounts, "SAC")
}

func (sacTransferMode) NewTargeter(ctx context.Context, rpcURL string, st *state.State, accounts accountLeaseManager) (vegeta.Targeter, error) {
	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	txSourceAccounts := accounts.Accounts(accountRequirementAnySource)
	if len(holderAccounts) < 2 {
		return nil, benchmarkTargeterCountError("SAC", 2, len(holderAccounts), "trustlined holder account")
	}
	if len(txSourceAccounts) == 0 {
		return nil, benchmarkTargeterCountError("SAC", 1, len(txSourceAccounts), "participant account")
	}
	for i, sac := range st.SACs {
		if sac == "" {
			return nil, benchmarkMissingContractIDError("SAC", fmt.Sprintf("SAC[%d]", i))
		}
	}

	holderCount := len(holderAccounts)

	// Decode SAC contract IDs from strkey C... to raw 32-byte arrays.
	var sacIDs [3]xdr.ContractId
	for i, sacStr := range st.SACs {
		raw, err := strkey.Decode(strkey.VersionByteContract, sacStr)
		if err != nil {
			return nil, fmt.Errorf("decode SAC[%d]: %w", i, err)
		}
		copy(sacIDs[i][:], raw)
	}

	// Convert classic assets to XDR for per-request footprint construction.
	var assetsXDR [3]xdr.Asset
	for i, a := range st.Assets {
		ax, err := a.ToXDR()
		if err != nil {
			return nil, fmt.Errorf("asset[%d] to XDR: %w", i, err)
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
	// simulator are kept as-is. Per-SAC Resources / ResourceFee / Ext are
	// preserved because protocol-23 autorestore can mark each SAC's archived
	// entries independently and the resource fee scales with whichever SAC
	// instance needs inline restoration.
	var (
		simResources          [3]xdr.SorobanResources
		simResourceFees       [3]xdr.Int64
		simFootprintTemplates [3]xdr.LedgerFootprint
		simDataExts           [3]xdr.SorobanTransactionDataExt
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

		simTemplate, err := presimulateSACTransfer(
			st, sacIDs[i],
			simTxSource, holderAccounts[0], holderAccounts[1],
		)
		if err != nil {
			return nil, fmt.Errorf("pre-simulate SAC[%d] transfer: %w", i, err)
		}
		simFootprintTemplates[i] = simTemplate.simulation.Footprint
		simResources[i] = simTemplate.simulation.Resources
		simResourceFees[i] = simTemplate.simulation.ResourceFee
		simDataExts[i] = simTemplate.simulation.Ext
	}

	// Pre-load the on-ledger sequence numbers for every participant account.
	// These serve as the base for per-account atomic counters that track
	// sequence progress independently.
	networkPassphrase := st.NetworkPassphrase

	return func(t *vegeta.Target) error {
		lease, err := accounts.Acquire(ctx, accountRequirementAnySource)
		if err != nil {
			return fmt.Errorf("lease SAC tx-source account: %w", err)
		}
		releaseLease := true
		defer func() {
			if releaseLease {
				accounts.ReleaseRetryable(lease.RequestID)
			}
		}()

		// Pick a random SAC and a source/destination holder pair.
		sacIdx := rand.IntN(len(st.SACs))
		sacID := sacIDs[sacIdx]
		assetXDR := assetsXDR[sacIdx]

		holderSrcIdx := rand.IntN(holderCount)
		dstIdx := rand.IntN(holderCount - 1)
		if dstIdx >= holderSrcIdx {
			dstIdx++
		}
		txSourceKP := lease.Account
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

		invokeArgs := buildSACTransferInvokeArgs(sacID, holderSrcAccID, dstAccID)

		footprint, dataExt, err := buildSACFootprintFromTemplate(simFootprintTemplates[sacIdx], simDataExts[sacIdx], assetXDR, holderSrcAccID, dstAccID)
		if err != nil {
			return fmt.Errorf("build SAC footprint: %w", err)
		}

		signers := []*keypair.Full{txSourceKP}
		if holderSrcKP.Address() != txSourceKP.Address() {
			signers = append(signers, holderSrcKP)
		}

		body, err := buildSorobanSendTransactionBody(sorobanSendTransactionParams{
			RPCID:             lease.RequestID,
			NetworkPassphrase: networkPassphrase,
			FeePayerKP:        st.FeePayerKP,
			TxSource:          txSourceKP,
			Sequence:          lease.Sequence,
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
				Instructions:  simResources[sacIdx].Instructions,
				DiskReadBytes: simResources[sacIdx].DiskReadBytes,
				WriteBytes:    simResources[sacIdx].WriteBytes,
			},
			ResourceFee: simResourceFees[sacIdx],
			// dataExt carries the remapped archivedSorobanEntries: positions
			// have shifted because buildSACFootprintFromTemplate drops the
			// template's per-asset trustline entries and appends the actual
			// src/dst trustlines. Dropped trustline indices are filtered
			// out (classic trustlines aren't persistent Soroban entries and
			// don't archive). Surviving indices -- typically the per-SAC
			// contract instance entry when the SAC is archived -- are
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

// presimulateSACTransfer builds a representative SAC transfer invocation and
// returns the padded simulation result plus the invoke args used to obtain it.
func presimulateSACTransfer(
	state *state.State,
	sacID xdr.ContractId,
	txSourceKP, srcKP, dstKP *keypair.Full,
) (simulatedInvocationTemplate, error) {
	srcAccID, err := xdr.AddressToAccountId(srcKP.Address())
	if err != nil {
		return simulatedInvocationTemplate{}, err
	}
	dstAccID, err := xdr.AddressToAccountId(dstKP.Address())
	if err != nil {
		return simulatedInvocationTemplate{}, err
	}

	return presimulateBenchmarkInvocation(state, txSourceKP, srcKP.Address(), buildSACTransferInvokeArgs(sacID, srcAccID, dstAccID))
}

func buildSACTransferInvokeArgs(sacID xdr.ContractId, srcAccID, dstAccID xdr.AccountId) xdr.InvokeContractArgs {
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
	return xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &sacID,
		},
		FunctionName: "transfer",
		Args:         args,
	}
}

// buildSACFootprintFromTemplate takes the footprint returned by the simulator for
// a representative transfer (accounts[0] -> accounts[1]) and substitutes the two
// ReadWrite trustline keys with the actual src/dst accounts for this request.
// All ReadOnly entries and non-trustline ReadWrite entries returned by the
// simulator are kept as-is since they are identical for every invocation.
//
// When the simulator emits a SorobanTransactionDataExt.V1 with
// archivedSorobanEntries (protocol-23 autorestore), the indices reference
// positions in the simulator's RW slice. Because we drop the template's
// per-asset trustline entries and append new src/dst trustlines at the tail,
// surviving indices must be remapped to point at the same entries' new
// positions. Dropped trustline indices are filtered out (classic trustlines
// don't archive and aren't persistent Soroban entries anyway). The returned
// extension is sorted in ascending order, which core requires.
func buildSACFootprintFromTemplate(
	tmpl xdr.LedgerFootprint,
	tmplExt xdr.SorobanTransactionDataExt,
	assetXDR xdr.Asset,
	src, dst xdr.AccountId,
) (xdr.LedgerFootprint, xdr.SorobanTransactionDataExt, error) {
	tla := xdr.TrustLineAsset{
		Type:       assetXDR.Type,
		AlphaNum4:  assetXDR.AlphaNum4,
		AlphaNum12: assetXDR.AlphaNum12,
	}
	footprint := xdr.LedgerFootprint{
		ReadOnly:  append([]xdr.LedgerKey(nil), tmpl.ReadOnly...),
		ReadWrite: make([]xdr.LedgerKey, 0, len(tmpl.ReadWrite)),
	}
	indexRemap := make([]int, len(tmpl.ReadWrite))
	for i, key := range tmpl.ReadWrite {
		if isTrustlineKeyForAsset(key, tla) {
			indexRemap[i] = -1
			continue
		}
		indexRemap[i] = len(footprint.ReadWrite)
		footprint.ReadWrite = append(footprint.ReadWrite, key)
	}
	footprint.ReadWrite = append(
		footprint.ReadWrite,
		xdr.LedgerKey{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: src, Asset: tla}},
		xdr.LedgerKey{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: dst, Asset: tla}},
	)
	newExt := remapArchivedSorobanEntries(tmplExt, indexRemap)
	return footprint, newExt, nil
}

// remapArchivedSorobanEntries translates archivedSorobanEntries indices from
// the simulator's original read-write footprint to their positions in a
// rewritten footprint. indexRemap[old] = new index, or < 0 if the entry was
// dropped. Returns SorobanTransactionDataExt.V0 if no surviving indices
// remain. Core requires the index list to be sorted ascending and to point
// only at persistent entries -- callers are responsible for the latter
// constraint by not dropping persistent entries during the rewrite.
func remapArchivedSorobanEntries(ext xdr.SorobanTransactionDataExt, indexRemap []int) xdr.SorobanTransactionDataExt {
	if ext.V != 1 || ext.ResourceExt == nil {
		return ext
	}
	old := ext.ResourceExt.ArchivedSorobanEntries
	if len(old) == 0 {
		return xdr.SorobanTransactionDataExt{V: 0}
	}
	remapped := make([]xdr.Uint32, 0, len(old))
	for _, idx := range old {
		i := int(idx)
		if i < 0 || i >= len(indexRemap) {
			continue
		}
		newIdx := indexRemap[i]
		if newIdx < 0 {
			continue
		}
		remapped = append(remapped, xdr.Uint32(newIdx))
	}
	return buildArchivedSorobanExt(remapped)
}

// buildArchivedSorobanExt assembles a SorobanTransactionDataExt from a set of
// read-write footprint indices that core must auto-restore. It sorts and
// dedups (core requires ascending, unique indices) and downgrades to V0 when
// the set is empty. Distinct source indices can collapse to the same new index
// if a rewrite merges entries, hence the dedup.
func buildArchivedSorobanExt(indices []xdr.Uint32) xdr.SorobanTransactionDataExt {
	if len(indices) == 0 {
		return xdr.SorobanTransactionDataExt{V: 0}
	}
	slices.Sort(indices)
	indices = slices.Compact(indices)
	return xdr.SorobanTransactionDataExt{
		V:           1,
		ResourceExt: &xdr.SorobanResourcesExtV0{ArchivedSorobanEntries: indices},
	}
}

func isTrustlineKeyForAsset(key xdr.LedgerKey, asset xdr.TrustLineAsset) bool {
	if key.Type != xdr.LedgerEntryTypeTrustline || key.TrustLine == nil {
		return false
	}
	return trustlineAssetsEqual(key.TrustLine.Asset, asset)
}

// trustlineAssetsEqual compares two TrustLineAsset values by their canonical
// XDR encoding. Go's `==` on AlphaNum4/AlphaNum12 walks the embedded AccountId,
// whose underlying PublicKey carries an `Ed25519 *Uint256` field; built-in
// struct equality compares those pointer addresses, not the 32 bytes they point
// at, so two assets with the same issuer parsed from separate XDR responses
// compare unequal. The marshal-and-compare path is robust against that.
func trustlineAssetsEqual(a, b xdr.TrustLineAsset) bool {
	if a.Type != b.Type {
		return false
	}
	aBytes, errA := a.MarshalBinary()
	bBytes, errB := b.MarshalBinary()
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(aBytes, bBytes)
}
