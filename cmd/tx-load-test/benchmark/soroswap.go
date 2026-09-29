package benchmark

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"

	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	sharedsoroswap "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroswap"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	soroswapSwapDeadlineWindow = 24 * time.Hour
	// Soroswap adds classic trustline keys after presimulation, so reserve
	// extra byte budget and fee headroom for each appended read-write key.
	soroswapAdditionalDiskReadBytesPerKey = uint32(256)
	soroswapAdditionalWriteBytesPerKey    = uint32(256)
	soroswapAdditionalResourceFeePerKey   = int64(10_000)
)

// soroswapMode is the Soroswap-swap benchmark workload.
//
// Each request is routed to one of the two independent liquidity pools with
// equal 50/50 probability. Because each pool's swap modifies only that pool's
// own contract instance storage entry the two pools are independent and can be
// processed by two separate apply threads simultaneously.
type soroswapMode struct{}

func (soroswapMode) Label() string { return "soroswap" }

func (soroswapMode) VerifyReady(ctx context.Context, st *state.State) error {
	if st.SoroswapFactoryContract == "" {
		return benchmarkMissingContractIDError("Soroswap", "soroswap factory")
	}
	if st.SoroswapRouterContract == "" {
		return benchmarkMissingContractIDError("Soroswap", "soroswap router")
	}
	if len(st.SoroswapPairContracts) != len(sharedsoroswap.BenchmarkPairs) {
		return fmt.Errorf("need %d Soroswap pair contracts, got %d -- rerun setup", len(sharedsoroswap.BenchmarkPairs), len(st.SoroswapPairContracts))
	}

	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holderAccounts) == 0 {
		return benchmarkStateCountError("Soroswap", 1, len(holderAccounts), "trustlined participant account")
	}
	if err := verifyTrustlineBalancesReady(ctx, st, holderAccounts, "Soroswap"); err != nil {
		return err
	}

	if _, err := requireReadyContract(ctx, st, "soroswap factory", st.SoroswapFactoryContract); err != nil {
		return err
	}
	if _, err := requireReadyContract(ctx, st, "soroswap router", st.SoroswapRouterContract); err != nil {
		return err
	}
	reportedFactory, err := sharedsoroswap.GetFactory(ctx, st, st.SoroswapRouterContract)
	if err != nil {
		return fmt.Errorf("validate soroswap router -> factory link: %w", err)
	}
	if reportedFactory != st.SoroswapFactoryContract {
		return fmt.Errorf("soroswap router %s points to factory %s, not %s", st.SoroswapRouterContract, reportedFactory, st.SoroswapFactoryContract)
	}

	for i, pair := range sharedsoroswap.BenchmarkPairs {
		pairContract := st.SoroswapPairContracts[i]
		if _, err := requireReadyContract(ctx, st, fmt.Sprintf("soroswap pair[%d]", i), pairContract); err != nil {
			return err
		}

		reserveA, err := sharedsoroswap.TokenBalance(ctx, st, st.SACs[pair[0]], pairContract)
		if err != nil {
			return fmt.Errorf("fetch soroswap pair[%d] reserve A: %w", i, err)
		}
		reserveB, err := sharedsoroswap.TokenBalance(ctx, st, st.SACs[pair[1]], pairContract)
		if err != nil {
			return fmt.Errorf("fetch soroswap pair[%d] reserve B: %w", i, err)
		}
		if !ledger.HasPositiveI128(reserveA) || !ledger.HasPositiveI128(reserveB) {
			return fmt.Errorf("soroswap pair[%d] is not seeded with positive reserves -- rerun setup", i)
		}
	}

	return nil
}

func (soroswapMode) NewTargeter(ctx context.Context, rpcURL string, st *state.State, accounts accountLeaseManager) (vegeta.Targeter, error) {
	txSourceAccounts := accounts.Accounts(accountRequirementTrustlinedSource)
	if len(txSourceAccounts) == 0 {
		return nil, benchmarkTargeterCountError("Soroswap", 1, len(txSourceAccounts), "participant account")
	}
	if st.SoroswapRouterContract == "" {
		return nil, benchmarkMissingContractIDError("Soroswap", "soroswap router")
	}
	if len(st.SoroswapPairContracts) != len(sharedsoroswap.BenchmarkPairs) {
		return nil, fmt.Errorf("need %d Soroswap pair contracts, got %d -- rerun setup", len(sharedsoroswap.BenchmarkPairs), len(st.SoroswapPairContracts))
	}
	if err := verifyTrustlineBalancesReady(ctx, st, txSourceAccounts, "Soroswap"); err != nil {
		return nil, err
	}

	templates, err := buildSoroswapSwapTemplates(ctx, st, txSourceAccounts)
	if err != nil {
		return nil, err
	}

	var slotCounter int64
	return func(t *vegeta.Target) error {
		slot := atomic.AddInt64(&slotCounter, 1) - 1
		lease, err := accounts.Acquire(ctx, accountRequirementTrustlinedSource)
		if err != nil {
			return fmt.Errorf("lease Soroswap tx-source account: %w", err)
		}
		releaseLease := true
		defer func() {
			if releaseLease {
				accounts.ReleaseRetryable(lease.RequestID)
			}
		}()

		templateIdx := int(slot % int64(len(templates)))
		txSourceKP := lease.Account
		tmpl := templates[templateIdx]

		invokeArgs, err := sharedsoroswap.RewriteInvokeContractAccount(tmpl.invokeArgs, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap invoke args: %w", err)
		}
		authEntries, err := sharedsoroswap.RewriteSorobanAuthEntriesAccount(tmpl.simulation.AuthEntries, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap auth: %w", err)
		}
		footprint, err := buildSoroswapFootprint(
			tmpl.simulation.Footprint,
			tmpl.traderAddress,
			txSourceKP.Address(),
			tmpl.inputAsset,
			tmpl.outputAsset,
		)
		if err != nil {
			return fmt.Errorf("build Soroswap footprint: %w", err)
		}
		resources, resourceFee := soroswapResourcesForFootprint(tmpl.simulation, footprint)

		body, err := buildSorobanSendTransactionBody(sorobanSendTransactionParams{
			RPCID:             lease.RequestID,
			NetworkPassphrase: st.NetworkPassphrase,
			TxSource:          txSourceKP,
			Sequence:          lease.Sequence,
			Signers:           []*keypair.Full{txSourceKP},
			OpSourceAccount:   txSourceKP.Address(),
			InvokeArgs:        invokeArgs,
			AuthEntries:       authEntries,
			Resources:         resources,
			ResourceFee:       resourceFee,
		})
		if err != nil {
			return err
		}
		populateJSONRPCTarget(t, rpcURL, body, lease.RequestID)
		releaseLease = false
		return nil
	}, nil
}

func soroswapResourcesForFootprint(simulated sharedsoroban.SimulatedInvocation, footprint xdr.LedgerFootprint) (xdr.SorobanResources, xdr.Int64) {
	resources := simulated.Resources
	resources.Footprint = footprint
	resourceFee := simulated.ResourceFee

	additionalReadWriteKeys := len(footprint.ReadWrite) - len(simulated.Footprint.ReadWrite)
	if additionalReadWriteKeys <= 0 {
		return resources, resourceFee
	}

	resources.DiskReadBytes += xdr.Uint32(additionalReadWriteKeys) * xdr.Uint32(soroswapAdditionalDiskReadBytesPerKey)
	resources.WriteBytes += xdr.Uint32(additionalReadWriteKeys) * xdr.Uint32(soroswapAdditionalWriteBytesPerKey)
	resourceFee += xdr.Int64(additionalReadWriteKeys) * xdr.Int64(soroswapAdditionalResourceFeePerKey)
	return resources, resourceFee
}

func buildSoroswapFootprint(
	tmpl xdr.LedgerFootprint,
	oldTraderAddress string,
	newTraderAddress string,
	inputAsset xdr.Asset,
	outputAsset xdr.Asset,
) (xdr.LedgerFootprint, error) {
	footprint, err := sharedsoroswap.RewriteFootprintAccount(tmpl, oldTraderAddress, newTraderAddress)
	if err != nil {
		return xdr.LedgerFootprint{}, err
	}

	traderID, err := xdr.AddressToAccountId(newTraderAddress)
	if err != nil {
		return xdr.LedgerFootprint{}, fmt.Errorf("parse trader account: %w", err)
	}
	inputTrustline, err := trustlineLedgerKey(traderID, inputAsset)
	if err != nil {
		return xdr.LedgerFootprint{}, fmt.Errorf("build input trustline key: %w", err)
	}
	outputTrustline, err := trustlineLedgerKey(traderID, outputAsset)
	if err != nil {
		return xdr.LedgerFootprint{}, fmt.Errorf("build output trustline key: %w", err)
	}

	return appendReadWriteKeysIfMissing(footprint, inputTrustline, outputTrustline)
}

func appendReadWriteKeysIfMissing(footprint xdr.LedgerFootprint, keys ...xdr.LedgerKey) (xdr.LedgerFootprint, error) {
	updated := xdr.LedgerFootprint{
		ReadOnly:  append([]xdr.LedgerKey(nil), footprint.ReadOnly...),
		ReadWrite: append([]xdr.LedgerKey(nil), footprint.ReadWrite...),
	}
	existing := make(map[string]struct{}, len(updated.ReadWrite))
	for _, key := range updated.ReadWrite {
		encoded, err := xdr.MarshalBase64(key)
		if err != nil {
			return xdr.LedgerFootprint{}, fmt.Errorf("marshal existing footprint key: %w", err)
		}
		existing[encoded] = struct{}{}
	}
	for _, key := range keys {
		encoded, err := xdr.MarshalBase64(key)
		if err != nil {
			return xdr.LedgerFootprint{}, fmt.Errorf("marshal appended footprint key: %w", err)
		}
		if _, ok := existing[encoded]; ok {
			continue
		}
		updated.ReadWrite = append(updated.ReadWrite, key)
		existing[encoded] = struct{}{}
	}
	return updated, nil
}

func trustlineLedgerKey(accountID xdr.AccountId, asset xdr.Asset) (xdr.LedgerKey, error) {
	if asset.Type != xdr.AssetTypeAssetTypeCreditAlphanum4 && asset.Type != xdr.AssetTypeAssetTypeCreditAlphanum12 {
		return xdr.LedgerKey{}, fmt.Errorf("unsupported trustline asset type %s", asset.Type)
	}
	tla := xdr.TrustLineAsset{
		Type:       asset.Type,
		AlphaNum4:  asset.AlphaNum4,
		AlphaNum12: asset.AlphaNum12,
	}
	return xdr.LedgerKey{Type: xdr.LedgerEntryTypeTrustline, TrustLine: &xdr.LedgerKeyTrustLine{AccountId: accountID, Asset: tla}}, nil
}
