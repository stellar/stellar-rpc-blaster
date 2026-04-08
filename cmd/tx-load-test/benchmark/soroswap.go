package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"

	sharedsoroswap "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroswap"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	soroswapSwapDeadlineWindow = 24 * time.Hour
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
		return fmt.Errorf("soroswap factory contract ID is empty -- run setup first")
	}
	if st.SoroswapRouterContract == "" {
		return fmt.Errorf("soroswap router contract ID is empty -- run setup first")
	}
	if len(st.SoroswapPairContracts) != len(sharedsoroswap.BenchmarkPairs) {
		return fmt.Errorf("need %d Soroswap pair contracts, got %d -- rerun setup", len(sharedsoroswap.BenchmarkPairs), len(st.SoroswapPairContracts))
	}

	holderAccounts := st.SACHolderKPs
	if len(holderAccounts) == 0 {
		holderAccounts = state.DefaultSACHolderKPs(st.AccountKPs)
	}
	if len(holderAccounts) == 0 {
		return fmt.Errorf("need trustlined participant accounts for Soroswap benchmark -- rerun setup")
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

func (soroswapMode) NewTargeter(ctx context.Context, rpcURL string, st *state.State, txSourceAccounts []*keypair.Full) (vegeta.Targeter, SequenceResetFunc, error) {
	if len(txSourceAccounts) == 0 {
		return nil, nil, fmt.Errorf("need at least 1 participant account for Soroswap tx sources")
	}
	if st.SoroswapRouterContract == "" {
		return nil, nil, fmt.Errorf("soroswap router contract ID is empty -- run setup first")
	}
	if len(st.SoroswapPairContracts) != len(sharedsoroswap.BenchmarkPairs) {
		return nil, nil, fmt.Errorf("need %d Soroswap pair contracts, got %d -- rerun setup", len(sharedsoroswap.BenchmarkPairs), len(st.SoroswapPairContracts))
	}
	if err := verifyTrustlineBalancesReady(ctx, st, txSourceAccounts, "Soroswap"); err != nil {
		return nil, nil, err
	}

	seqs, err := newSequenceManager(ctx, st, txSourceAccounts, "Soroswap tx-source")
	if err != nil {
		return nil, nil, err
	}

	templates, err := buildSoroswapSwapTemplates(ctx, st, txSourceAccounts)
	if err != nil {
		return nil, nil, err
	}

	var slotCounter int64
	return func(t *vegeta.Target) error {
		slot := atomic.AddInt64(&slotCounter, 1) - 1
		srcIdx := int(slot % int64(len(txSourceAccounts)))
		templateIdx := int((slot / int64(len(txSourceAccounts))) % int64(len(templates)))
		txSourceKP := txSourceAccounts[srcIdx]
		seq := seqs.Next(srcIdx)
		tmpl := templates[templateIdx]

		invokeArgs, err := sharedsoroswap.RewriteInvokeContractAccount(tmpl.invokeArgs, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap invoke args: %w", err)
		}
		authEntries, err := sharedsoroswap.RewriteSorobanAuthEntriesAccount(tmpl.authEntries, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap auth: %w", err)
		}
		footprint, err := sharedsoroswap.RewriteFootprintAccount(tmpl.footprint, tmpl.traderAddress, txSourceKP.Address())
		if err != nil {
			return fmt.Errorf("rewrite Soroswap footprint: %w", err)
		}

		op := txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type:           xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &invokeArgs,
			},
			Auth:          authEntries,
			SourceAccount: txSourceKP.Address(),
			Ext: xdr.TransactionExt{
				V: 1,
				SorobanData: &xdr.SorobanTransactionData{
					Resources: xdr.SorobanResources{
						Footprint:     footprint,
						Instructions:  tmpl.resources.Instructions,
						DiskReadBytes: tmpl.resources.DiskReadBytes,
						WriteBytes:    tmpl.resources.WriteBytes,
					},
					ResourceFee: tmpl.resourceFee,
				},
			},
		}

		tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        &txnbuild.SimpleAccount{AccountID: txSourceKP.Address(), Sequence: seq},
			IncrementSequenceNum: false,
			Operations:           []txnbuild.Operation{&op},
			BaseFee:              benchmarkBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
		})
		if err != nil {
			return fmt.Errorf("build Soroswap transaction: %w", err)
		}
		tx, err = tx.Sign(st.NetworkPassphrase, txSourceKP)
		if err != nil {
			return fmt.Errorf("sign Soroswap transaction: %w", err)
		}
		b64, err := tx.Base64()
		if err != nil {
			return fmt.Errorf("marshal Soroswap transaction: %w", err)
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
