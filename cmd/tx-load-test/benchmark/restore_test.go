package benchmark

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	sharedsoroban "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroban"
	sharedsoroswap "github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/soroswap"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

func TestSelectAccountRange(t *testing.T) {
	accounts := []*keypair.Full{mustRandomKP(t), mustRandomKP(t), mustRandomKP(t), mustRandomKP(t)}

	selected, start, end, err := selectAccountRange(accounts, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 1, start)
	require.Equal(t, 3, end)
	require.Equal(t, accounts[1:3], selected)

	selected, start, end, err = selectAccountRange(accounts, 2, 0)
	require.NoError(t, err)
	require.Equal(t, 2, start)
	require.Equal(t, 4, end)
	require.Equal(t, accounts[2:], selected)

	_, _, _, err = selectAccountRange(accounts, 4, 0)
	require.ErrorContains(t, err, "outside account pool")
}

func TestRestoreSACContractsProbesContractInstancesOnly(t *testing.T) {
	oldProbe := restoreInvokeContract
	defer func() { restoreInvokeContract = oldProbe }()

	var calls []xdr.InvokeContractArgs
	restoreInvokeContract = func(
		_ context.Context,
		_ *state.State,
		_ *keypair.Full,
		_ string,
		invokeArgs xdr.InvokeContractArgs,
		_ int64,
		_ sharedsoroban.RestoreProbeOptions,
	) (sharedsoroban.RestoreProbeResult, error) {
		calls = append(calls, invokeArgs)
		return sharedsoroban.RestoreProbeResult{}, nil
	}

	st := &state.State{
		FeePayerKP:        mustRandomKP(t),
		NetworkPassphrase: network.TestNetworkPassphrase,
		SACs:              [3]string{mustContractID(t, 1), mustContractID(t, 2), mustContractID(t, 3)},
	}
	summary, err := restoreSACContracts(context.Background(), st, RestoreOptions{DryRun: true}, restoreSummary{
		mode:      config.ModeSACTransfer,
		dryRun:    true,
		startedAt: time.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, 3, summary.probes)
	require.Len(t, calls, 3)
	for _, call := range calls {
		require.Equal(t, xdr.ScSymbol("decimals"), call.FunctionName)
		require.Empty(t, call.Args)
	}
}

func TestRestoreDryRunLogsPerModeSummary(t *testing.T) {
	oldProbe := restoreInvokeContract
	defer func() { restoreInvokeContract = oldProbe }()

	restoreInvokeContract = func(
		_ context.Context,
		_ *state.State,
		_ *keypair.Full,
		_ string,
		invokeArgs xdr.InvokeContractArgs,
		_ int64,
		options sharedsoroban.RestoreProbeOptions,
	) (sharedsoroban.RestoreProbeResult, error) {
		require.True(t, options.DryRun)
		return sharedsoroban.RestoreProbeResult{
			RestoreNeeded: true,
			ReadOnlyKeys:  1,
			ReadWriteKeys: 2,
			ResourceFee:   3,
		}, nil
	}

	var buf bytes.Buffer
	logger := log.New()
	logger.SetLevel(log.InfoLevel)
	logger.SetOutput(&buf)
	logger.DisableColors()
	logger.DisableTimestamp()

	accounts := []*keypair.Full{mustRandomKP(t), mustRandomKP(t)}
	st := &state.State{
		FeePayerKP:        mustRandomKP(t),
		NetworkPassphrase: network.TestNetworkPassphrase,
		AccountKPs:        accounts,
		OZTokenContract:   mustContractID(t, 4),
	}
	err := RestoreArchivedState(context.Background(), logger, st, RestoreOptions{
		Mode:             string(config.ModeOZTransfer),
		DryRun:           true,
		AccountLimit:     2,
		ProgressInterval: 1,
	})
	require.NoError(t, err)

	output := buf.String()
	require.Contains(t, output, "restore mode started")
	require.Contains(t, output, "restore progress")
	require.Contains(t, output, "restore dry-run summary: would restore archived state where needed")
	require.Contains(t, output, "mode=oz-transfer")
	require.Contains(t, output, "totalProbes=2")
	require.Contains(t, output, "probes=2")
	require.Contains(t, output, "restoreNeededProbes=2")
	require.Contains(t, output, "restoreTransactions=0")
	require.Contains(t, output, "readOnlyKeys=2")
	require.Contains(t, output, "readWriteKeys=4")
	require.False(t, strings.Contains(output, "restore summary\n"), output)
}

func TestRestoreAccountLimitAppliesPerModeAccountSelection(t *testing.T) {
	oldProbe := restoreInvokeContract
	oldTokenBalance := restoreSoroswapTokenBalance
	defer func() {
		restoreInvokeContract = oldProbe
		restoreSoroswapTokenBalance = oldTokenBalance
	}()

	var soroswapAmounts []xdr.Uint64
	restoreInvokeContract = func(
		_ context.Context,
		_ *state.State,
		_ *keypair.Full,
		_ string,
		invokeArgs xdr.InvokeContractArgs,
		_ int64,
		_ sharedsoroban.RestoreProbeOptions,
	) (sharedsoroban.RestoreProbeResult, error) {
		if invokeArgs.FunctionName == "swap_exact_tokens_for_tokens" {
			amount, ok := invokeArgs.Args[0].GetI128()
			require.True(t, ok)
			soroswapAmounts = append(soroswapAmounts, amount.Lo)
		}
		return sharedsoroban.RestoreProbeResult{}, nil
	}
	restoreSoroswapTokenBalance = func(context.Context, *state.State, string, string) (xdr.Int128Parts, error) {
		return xdr.Int128Parts{Hi: 0, Lo: 1_000_000}, nil
	}

	accounts := []*keypair.Full{mustRandomKP(t), mustRandomKP(t), mustRandomKP(t), mustRandomKP(t)}
	st := &state.State{
		FeePayerKP:              mustRandomKP(t),
		NetworkPassphrase:       network.TestNetworkPassphrase,
		AccountKPs:              accounts,
		SACHolderKPs:            accounts,
		SACs:                    [3]string{mustContractID(t, 1), mustContractID(t, 2), mustContractID(t, 3)},
		OZTokenContract:         mustContractID(t, 4),
		SoroswapFactoryContract: mustContractID(t, 5),
		SoroswapRouterContract:  mustContractID(t, 6),
		SoroswapPairContracts:   []string{mustContractID(t, 7), mustContractID(t, 8)},
	}

	ozSummary, err := restoreOZBalances(context.Background(), st, RestoreOptions{AccountLimit: 2, DryRun: true}, restoreSummary{
		mode:      config.ModeOZTransfer,
		dryRun:    true,
		startedAt: time.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, 2, ozSummary.selectedAccounts)
	require.Equal(t, 2, ozSummary.totalProbes)
	require.Equal(t, 2, ozSummary.probes)

	soroswapSummary, err := restoreSoroswap(context.Background(), st, RestoreOptions{AccountLimit: 2, DryRun: true}, restoreSummary{
		mode:      config.ModeSoroswap,
		dryRun:    true,
		startedAt: time.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, 2, soroswapSummary.selectedAccounts)
	require.Equal(t, 2*len(sharedsoroswap.BenchmarkPairs)*2, soroswapSummary.totalProbes)
	require.Equal(t, soroswapSummary.totalProbes, soroswapSummary.probes)
	require.Len(t, soroswapAmounts, soroswapSummary.probes)
	for _, amount := range soroswapAmounts {
		require.Equal(t, xdr.Uint64(1000), amount)
	}
}

func TestRestoreStopsAfterContextCancellation(t *testing.T) {
	oldProbe := restoreInvokeContract
	defer func() { restoreInvokeContract = oldProbe }()

	ctx, cancel := context.WithCancel(context.Background())
	probeCalls := 0
	restoreInvokeContract = func(
		_ context.Context,
		_ *state.State,
		_ *keypair.Full,
		_ string,
		_ xdr.InvokeContractArgs,
		_ int64,
		_ sharedsoroban.RestoreProbeOptions,
	) (sharedsoroban.RestoreProbeResult, error) {
		probeCalls++
		cancel()
		return sharedsoroban.RestoreProbeResult{}, nil
	}

	accounts := []*keypair.Full{mustRandomKP(t), mustRandomKP(t), mustRandomKP(t), mustRandomKP(t)}
	st := &state.State{
		FeePayerKP:        mustRandomKP(t),
		NetworkPassphrase: network.TestNetworkPassphrase,
		AccountKPs:        accounts,
		OZTokenContract:   mustContractID(t, 4),
	}
	summary, err := restoreOZBalances(ctx, st, RestoreOptions{AccountLimit: 4, DryRun: true}, restoreSummary{
		mode:      config.ModeOZTransfer,
		dryRun:    true,
		startedAt: time.Now(),
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, probeCalls)
	require.Equal(t, 1, summary.probes)
}

func mustRandomKP(t *testing.T) *keypair.Full {
	t.Helper()
	kp, err := keypair.Random()
	require.NoError(t, err)
	return kp
}

func mustContractID(t *testing.T, marker byte) string {
	t.Helper()
	var id xdr.ContractId
	id[0] = marker
	encoded, err := ledger.EncodeContractID(id)
	require.NoError(t, err)
	return encoded
}
