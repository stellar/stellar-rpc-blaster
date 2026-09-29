package extendttl

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// fakeEntriesClient serves canned ledger entries keyed by base64 LedgerKey.
type fakeEntriesClient struct {
	entries map[string]protocol.LedgerEntryResult
}

func (f *fakeEntriesClient) GetLedgerEntries(
	_ context.Context, req protocol.GetLedgerEntriesRequest,
) (protocol.GetLedgerEntriesResponse, error) {
	var resp protocol.GetLedgerEntriesResponse
	for _, key := range req.Keys {
		if entry, ok := f.entries[key]; ok {
			resp.Entries = append(resp.Entries, entry)
		}
	}
	return resp, nil
}

func testContractID(fill byte) xdr.ContractId {
	var id xdr.ContractId
	for i := range id {
		id[i] = fill
	}
	return id
}

func mustB64(t *testing.T, key xdr.LedgerKey) string {
	t.Helper()
	b64, err := xdr.MarshalBase64(key)
	require.NoError(t, err)
	return b64
}

// addEntry registers a canned response for key with the given data/liveUntil.
func addEntry(t *testing.T, f *fakeEntriesClient, key xdr.LedgerKey, data xdr.LedgerEntryData, liveUntil uint32) {
	t.Helper()
	dataB64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	keyB64 := mustB64(t, key)
	entry := protocol.LedgerEntryResult{KeyXDR: keyB64, DataXDR: dataB64}
	if liveUntil > 0 {
		entry.LiveUntilLedgerSeq = &liveUntil
	}
	f.entries[keyB64] = entry
}

func instanceData(contractID xdr.ContractId, exec xdr.ContractExecutable) xdr.LedgerEntryData {
	return xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract:   ledger.ContractScAddress(contractID),
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
			Val: xdr.ScVal{
				Type:     xdr.ScValTypeScvContractInstance,
				Instance: &xdr.ScContractInstance{Executable: exec},
			},
		},
	}
}

func balanceData(contractID xdr.ContractId) xdr.LedgerEntryData {
	i128 := xdr.Int128Parts{Lo: 42}
	sym := xdr.ScSymbol("Balance")
	return xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract:   ledger.ContractScAddress(contractID),
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym},
			Durability: xdr.ContractDataDurabilityPersistent,
			Val:        xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &i128},
		},
	}
}

func TestResolveAndClassify(t *testing.T) {
	const (
		latestLedger uint32 = 1_000_000
		extendTo     uint32 = 100_000
	)

	ozID := testContractID(0x01)
	sacID := testContractID(0x02)
	var wasmHash xdr.Hash
	for i := range wasmHash {
		wasmHash[i] = 0x77
	}

	ozKey := ledger.ContractInstanceLedgerKey(ozID)
	sacKey := ledger.ContractInstanceLedgerKey(sacID)
	missingKey := ledger.ContractInstanceLedgerKey(testContractID(0x03))
	archivedBalKey := ledger.ContractBalanceLedgerKey(sacID, ledger.ContractScAddress(ozID))

	fake := &fakeEntriesClient{entries: map[string]protocol.LedgerEntryResult{}}
	// OZ instance: wasm executable, below extend target -> needs extend; its
	// wasm code entry is discovered and already live long enough.
	addEntry(t, fake, ozKey, instanceData(ozID, xdr.ContractExecutable{
		Type:     xdr.ContractExecutableTypeContractExecutableWasm,
		WasmHash: &wasmHash,
	}), latestLedger+extendTo/2)
	addEntry(t, fake, ledger.ContractCodeLedgerKey(wasmHash),
		xdr.LedgerEntryData{
			Type:         xdr.LedgerEntryTypeContractCode,
			ContractCode: &xdr.ContractCodeEntry{Hash: wasmHash},
		}, latestLedger+extendTo*2)
	// SAC instance: native executable (no wasm discovery), needs extend.
	addEntry(t, fake, sacKey, instanceData(sacID, xdr.ContractExecutable{
		Type: xdr.ContractExecutableTypeContractExecutableStellarAsset,
	}), latestLedger+1)
	// Balance entry that has already expired -> archived.
	addEntry(t, fake, archivedBalKey, balanceData(sacID), latestLedger-5)

	items := []item{
		{label: "oz instance", key: ozKey, infra: true},
		{label: "sac instance", key: sacKey, infra: true},
		{label: "missing instance", key: missingKey, infra: true},
		{label: "archived balance", key: archivedBalKey},
	}

	classified, err := resolveAndClassify(context.Background(), fake, items, latestLedger, extendTo)
	require.NoError(t, err)

	byLabel := map[string]item{}
	for _, it := range classified {
		byLabel[it.label] = it
	}

	require.Equal(t, categoryNeedsExtend, byLabel["oz instance"].category)
	require.Equal(t, categoryNeedsExtend, byLabel["sac instance"].category)
	require.Equal(t, categoryMissing, byLabel["missing instance"].category)
	require.Equal(t, categoryArchived, byLabel["archived balance"].category)

	// Wasm discovered from the OZ instance, appended, and classified live.
	wasmLabel := fmt.Sprintf("wasm %x (from oz instance)", wasmHash[:4])
	require.Contains(t, byLabel, wasmLabel)
	require.Equal(t, categoryLiveEnough, byLabel[wasmLabel].category)
	require.True(t, byLabel[wasmLabel].infra)

	// Exactly one wasm entry despite two instances (SAC contributes none).
	require.Len(t, classified, 5)
}

func TestResolveAndClassifyBoundaries(t *testing.T) {
	// extendTo > slack so the slack-adjusted boundary is exercised: entries
	// within extendSlackLedgers of the target classify live (keeps re-runs
	// no-ops), anything below that needs extension.
	const (
		latestLedger uint32 = 500_000
		extendTo     uint32 = extendSlackLedgers + 1_000
	)
	sacID := testContractID(0x09)

	cases := []struct {
		name      string
		liveUntil uint32
		want      category
	}{
		{"exactly latest is archived", latestLedger, categoryArchived},
		{"one past latest needs extend", latestLedger + 1, categoryNeedsExtend},
		{"one below slack boundary needs extend", latestLedger + extendTo - extendSlackLedgers - 1, categoryNeedsExtend},
		{"at slack boundary is live", latestLedger + extendTo - extendSlackLedgers, categoryLiveEnough},
		{"exactly target is live", latestLedger + extendTo, categoryLiveEnough},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := ledger.ContractInstanceLedgerKey(sacID)
			fake := &fakeEntriesClient{entries: map[string]protocol.LedgerEntryResult{}}
			addEntry(t, fake, key, instanceData(sacID, xdr.ContractExecutable{
				Type: xdr.ContractExecutableTypeContractExecutableStellarAsset,
			}), tc.liveUntil)

			classified, err := resolveAndClassify(context.Background(), fake,
				[]item{{label: "e", key: key}}, latestLedger, extendTo)
			require.NoError(t, err)
			require.Len(t, classified, 1)
			require.Equal(t, tc.want, classified[0].category)
		})
	}
}

func TestEnumerateSkipBalances(t *testing.T) {
	encode := func(fill byte) string {
		s, err := ledger.EncodeContractID(testContractID(fill))
		require.NoError(t, err)
		return s
	}
	kp1 := keypair.MustRandom()
	kp2 := keypair.MustRandom()
	st := &state.State{
		OZTokenContract:         encode(0x01),
		SoroswapRouterContract:  encode(0x02),
		SoroswapFactoryContract: encode(0x03),
		SACs:                    [3]string{encode(0x04), encode(0x05), encode(0x06)},
		SoroswapPairContracts:   []string{encode(0x07), encode(0x08)},
		AccountKPs:              []*keypair.Full{kp1, kp2},
	}

	// 3 named + 3 sac + 2 pair instances, 2*3 pair fund balances = 14 infra.
	const infraCount = 14

	full, err := enumerate(st, false)
	require.NoError(t, err)
	require.Len(t, full, infraCount+len(st.AccountKPs))
	balances := 0
	for _, it := range full {
		if !it.infra {
			balances++
			require.Contains(t, it.label, "oz balance ")
		}
	}
	require.Equal(t, len(st.AccountKPs), balances)

	skipped, err := enumerate(st, true)
	require.NoError(t, err)
	require.Len(t, skipped, infraCount)
	for _, it := range skipped {
		require.True(t, it.infra)
	}
}

// TestClampExtendTo pins the off-by-one: core rejects ExtendTo == maxEntryTTL
// ("TTL extension is too large: 3110400 > 3110399") because the current
// ledger counts toward liveness. The default flag value (180 days) is exactly
// maxEntryTTL and MUST clamp below it.
func TestClampExtendTo(t *testing.T) {
	require.Equal(t, uint32(MaxExtendToLedgers), clampExtendTo(MaxEntryTTLLedgers, MaxEntryTTLLedgers))
	require.Equal(t, uint32(MaxExtendToLedgers), clampExtendTo(MaxEntryTTLLedgers+500_000, MaxEntryTTLLedgers))
	require.Equal(t, uint32(MaxExtendToLedgers), clampExtendTo(MaxExtendToLedgers, MaxEntryTTLLedgers))
	require.Equal(t, uint32(1), clampExtendTo(1, MaxEntryTTLLedgers))

	// The CLI default: 180 days * 17280 ledgers/day == maxEntryTTL exactly.
	defaultExtendTo := uint32(180 * LedgersPerDay)
	require.Equal(t, uint32(MaxEntryTTLLedgers), defaultExtendTo)
	require.Less(t, clampExtendTo(defaultExtendTo, MaxEntryTTLLedgers), defaultExtendTo)

	// A network with a smaller cap clamps to that cap - 1.
	require.Equal(t, uint32(99_999), clampExtendTo(defaultExtendTo, 100_000))
	// A zero (failed-fetch sentinel misuse) cap must not underflow.
	require.Equal(t, defaultExtendTo, clampExtendTo(defaultExtendTo, 0))
}

func TestFetchNetworkMaxEntryTTL(t *testing.T) {
	maxTTL := xdr.Uint32(3_110_400)
	data := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeConfigSetting,
		ConfigSetting: &xdr.ConfigSettingEntry{
			ConfigSettingId:       xdr.ConfigSettingIdConfigSettingStateArchival,
			StateArchivalSettings: &xdr.StateArchivalSettings{MaxEntryTtl: maxTTL},
		},
	}
	key := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeConfigSetting,
		ConfigSetting: &xdr.LedgerKeyConfigSetting{
			ConfigSettingId: xdr.ConfigSettingIdConfigSettingStateArchival,
		},
	}
	fake := &fakeEntriesClient{entries: map[string]protocol.LedgerEntryResult{}}
	addEntry(t, fake, key, data, 0)

	got, err := fetchNetworkMaxEntryTTL(context.Background(), fake)
	require.NoError(t, err)
	require.Equal(t, uint32(3_110_400), got)

	empty := &fakeEntriesClient{entries: map[string]protocol.LedgerEntryResult{}}
	_, err = fetchNetworkMaxEntryTTL(context.Background(), empty)
	require.Error(t, err)
}

func TestFormatXLM(t *testing.T) {
	require.Equal(t, "0.0000001", formatXLM(1))
	require.Equal(t, "1.5000000", formatXLM(15_000_000))
	require.Equal(t, "-0.5000000", formatXLM(-5_000_000))
	require.Equal(t, "0.0000000", formatXLM(0))
}

func TestArchivedError(t *testing.T) {
	require.NoError(t, archivedError(nil, 0))

	items := []item{
		{label: "b entry", category: categoryArchived},
		{label: "a entry", category: categoryArchived},
		{label: "fine", category: categoryLiveEnough},
	}
	err := archivedError(items, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "a entry, b entry")
	require.Contains(t, err.Error(), "--restore-archived")

	// Overflow past the listing cap is summarized, not enumerated.
	many := make([]item, 0, maxListedPerCategory+10)
	for i := 0; i < maxListedPerCategory+10; i++ {
		many = append(many, item{label: fmt.Sprintf("entry-%03d", i), category: categoryArchived})
	}
	err = archivedError(many, len(many))
	require.Error(t, err)
	require.Contains(t, err.Error(), "and 10 more")
}
