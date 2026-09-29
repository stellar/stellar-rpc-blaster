package benchmark

import (
	"context"
	"fmt"
	"math/rand/v2"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// benchmarkBaseFeeMin and benchmarkBaseFeeMax bound the per-op inclusion fee
// (in stroops) randomly chosen for each submitted benchmark transaction.
//
// Two reasons we sample per-tx instead of using a single fixed value:
//
//  1. Stellar core's SurgePricingUtils::computeBetterFee requires a STRICTLY
//     greater per-op fee for a new tx to evict (or beat in per-ledger
//     inclusion ranking) one already in the queue. With identical bids,
//     every tx after the first ties and is rejected with txINSUFFICIENT_FEE
//     — observed at 17,666 of 18,000 OZ-transfer submits in a 5-min bench.
//     Per-tx random sampling makes ties statistically vanishing (collision
//     rate ~= queue_depth^2 / (2*range_size); 90,001 distinct buckets here).
//     This is the same primitive stellar-core's own LoadGenerator
//     (TxGenerator::generateFee) uses.
//
//  2. To stay above network surge floors. The [10_000, 100_000] range is
//     a heuristic starting point: well above the 100-stroop network
//     minimum and historical futurenet external-surge p95s in the
//     low-thousands. It is NOT guaranteed to clear self-induced surge on
//     resource-heavy workloads — at high RPS for OZ/soroswap the bench
//     can saturate ledger Soroban capacity, the surge floor rises tx-by-
//     tx, and the upper bound here can be exceeded. Cost on test networks
//     is negligible: a 100k-stroop inclusion bid is ~0.01 XLM per tx,
//     and the fee_payer holds free XLM. For pubnet, or for sustained
//     high-RPS Soroban-heavy benches, the operator should re-tune these
//     constants based on observed getFeeStats.SorobanInclusionFee.
const (
	benchmarkBaseFeeMin int64 = 10_000
	benchmarkBaseFeeMax int64 = 100_000
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

		simTemplate, err := presimulateSACTransfer(
			st, sacIDs[i],
			simTxSource, holderAccounts[0], holderAccounts[1],
		)
		if err != nil {
			return nil, fmt.Errorf("pre-simulate SAC[%d] transfer: %w", i, err)
		}
		simFootprintTemplates[i] = simTemplate.simulation.Footprint
		// All three SACs share the same WASM and logic; use the last
		// simulation's resource numbers (they should be identical).
		simResources = simTemplate.simulation.Resources
		simResourceFee = simTemplate.simulation.ResourceFee
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

		footprint, err := buildSACFootprintFromTemplate(simFootprintTemplates[sacIdx], assetXDR, holderSrcAccID, dstAccID)
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
				Instructions:  simResources.Instructions,
				DiskReadBytes: simResources.DiskReadBytes,
				WriteBytes:    simResources.WriteBytes,
			},
			ResourceFee: simResourceFee,
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

	return presimulateBenchmarkInvocation(state, txSourceKP, srcKP.Address(), invokeArgs)
}

// buildSACFootprintFromTemplate takes the footprint returned by the simulator for
// a representative transfer (accounts[0] -> accounts[1]) and substitutes the two
// ReadWrite trustline keys with the actual src/dst accounts for this request.
// All ReadOnly entries (contract instance, issuer account, source account read
// for auth) are kept as-is since they are identical for every invocation.
func buildSACFootprintFromTemplate(
	tmpl xdr.LedgerFootprint,
	assetXDR xdr.Asset,
	src, dst xdr.AccountId,
) (xdr.LedgerFootprint, error) {
	tla := xdr.TrustLineAsset{
		Type:       assetXDR.Type,
		AlphaNum4:  assetXDR.AlphaNum4,
		AlphaNum12: assetXDR.AlphaNum12,
	}
	return buildFootprintFromTemplate(
		tmpl,
		func() (xdr.LedgerKey, error) {
			return xdr.LedgerKey{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: src, Asset: tla}}, nil
		},
		func() (xdr.LedgerKey, error) {
			return xdr.LedgerKey{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: dst, Asset: tla}}, nil
		},
	)
}
