// Package extendttl extends the TTL of every Soroban ledger entry a benchmark
// run can touch, so entries never cross the archival boundary between runs.
//
// Why this exists: several footprint entries are extended by NOTHING at
// invocation time -- the OZ token instance (the OpenZeppelin library defines
// instance-TTL constants but never calls extend), the OZ Wasm code entry
// (code entries cannot be extended by contract code at all), and the SAC
// instances (the host's SAC implementation only extends balance and allowance
// entries). Once any of these archives, every workload fails at benchmark
// startup with a RestorePreamble during template simulation. Everything else
// (OZ balances, pair instances, pair SAC fund balances) IS extended on touch,
// but only by ~30 days -- so an idle gap longer than that archives the whole
// hot set at once. A periodic extend pass removes both failure modes.
//
// The entry set is enumerated deterministically from the state file plus the
// FEE_PAYER-derived account pool; the chain is only consulted for current
// TTLs (and to discover Wasm hashes from instance entries), never for which
// entries exist.
package extendttl

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	// LedgersPerDay converts day-denominated flags to ledgers (5s ledgers).
	LedgersPerDay = 17_280

	// MaxEntryTTLLedgers is the network maxEntryTTL cap on mainnet and testnet
	// (~180 days).
	MaxEntryTTLLedgers = 3_110_400

	// MaxExtendToLedgers is the largest ExtendTo value core accepts. An entry
	// is live on the current ledger too, so the highest reachable liveUntil is
	// current + maxEntryTTL - 1; an ExtendTo of exactly maxEntryTTL fails with
	// "TTL extension is too large: 3110400 > 3110399". Clamping here keeps the
	// default 180-day flag value (180 * 17280 = maxEntryTTL exactly) valid
	// without a config fetch.
	MaxExtendToLedgers = MaxEntryTTLLedgers - 1

	// DefaultBatchSize is the number of read-only footprint keys per
	// ExtendFootprintTtl transaction. Conservative with respect to per-tx
	// read-entry and disk-read-byte limits; raise it if the network's limits
	// are known to allow more.
	DefaultBatchSize = 25

	// extendSlackLedgers treats entries within this many ledgers of the extend
	// target as live. TTLs decay every ledger, so without slack an entry
	// extended moments ago already sits below the (moving) target and every
	// re-run would re-extend the entire set; with it, back-to-back runs are
	// no-ops.
	extendSlackLedgers = LedgersPerDay

	// maxListedPerCategory caps how many entries are printed per category so a
	// pool-wide condition doesn't scroll thousands of identical lines.
	maxListedPerCategory = 25
)

// Options configures an extend-ttl pass.
type Options struct {
	// ExtendToLedgers is the target TTL, in ledgers from the current ledger.
	ExtendToLedgers uint32
	// BatchSize is the number of footprint keys per transaction.
	BatchSize int
	// DryRun reports the classification without submitting transactions.
	DryRun bool
	// RestoreArchived submits RestoreFootprint transactions for entries that
	// are already archived (which extend cannot touch) before extending. On
	// protocol 23 this is the only tool path that heals archived state: the
	// restore subcommand's probe flow sees every archived entry as
	// autorestore-class and submits nothing. Restored entries come back at the
	// network minimum persistent TTL (~120 days) and are then topped up to the
	// extend target like everything else.
	RestoreArchived bool
	// SkipBalances excludes the per-account OZ balance entries, extending only
	// the infra set (instances, wasm, pair funds). Balance entries self-extend
	// ~30 days on every bench touch (rent paid inside the bench transactions),
	// so prepaying their rent only makes sense ahead of a planned idle gap
	// longer than that; the infra set never self-extends and is the reason
	// this command exists. Cost difference is roughly two orders of magnitude.
	SkipBalances bool
}

// batchLogger decorates the logger with the operation and batch context so
// the generic submit/poll lines emitted downstream (e.g. "1/1 transactions
// submitted", "confirmed 1/1") identify which pass and which entries they
// belong to.
func batchLogger(logger *log.Entry, op string, batch []item, start, end, total int) *log.Entry {
	infra, balances := 0, 0
	for _, it := range batch {
		if it.infra {
			infra++
		} else {
			balances++
		}
	}
	return logger.WithFields(log.F{
		"op":             op,
		"batch":          fmt.Sprintf("%d-%d/%d", start, end-1, total),
		"infraEntries":   infra,
		"balanceEntries": balances,
	})
}

// effectiveTarget lowers the extend target by the slack when classifying, so
// entries extended recently (or by a concurrent touch) count as live instead
// of being re-extended on every run.
func effectiveTarget(extendTo uint32) uint32 {
	if extendTo <= extendSlackLedgers {
		return extendTo
	}
	return extendTo - extendSlackLedgers
}

// clampExtendTo caps an ExtendTo value at the largest extension core accepts
// (the network's maxEntryTTL - 1; the current ledger counts toward liveness,
// so maxEntryTTL itself is an off-by-one rejection -- observed on mainnet as
// "TTL extension is too large: 3110400 > 3110399").
func clampExtendTo(extendTo, maxEntryTTL uint32) uint32 {
	if maxEntryTTL == 0 {
		return extendTo
	}
	if extendTo > maxEntryTTL-1 {
		return maxEntryTTL - 1
	}
	return extendTo
}

// fetchNetworkMaxEntryTTL reads maxEntryTTL from the network's stateArchival
// config setting entry, so the extend-to clamp tracks the actual network
// rather than the hardcoded mainnet/testnet value (custom networks can run
// smaller caps, and the cap can change in a network upgrade).
func fetchNetworkMaxEntryTTL(ctx context.Context, rpc ledger.LedgerEntriesClient) (uint32, error) {
	key := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeConfigSetting,
		ConfigSetting: &xdr.LedgerKeyConfigSetting{
			ConfigSettingId: xdr.ConfigSettingIdConfigSettingStateArchival,
		},
	}
	b64, err := xdr.MarshalBase64(key)
	if err != nil {
		return 0, fmt.Errorf("marshal stateArchival config key: %w", err)
	}
	entries, err := ledger.FetchLedgerEntriesByKey(ctx, rpc, []string{b64}, 1)
	if err != nil {
		return 0, fmt.Errorf("get stateArchival config entry: %w", err)
	}
	entry, ok := entries[b64]
	if !ok {
		return 0, fmt.Errorf("stateArchival config entry not found")
	}
	var data xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(entry.DataXDR, &data); err != nil {
		return 0, fmt.Errorf("decode stateArchival config entry: %w", err)
	}
	if data.ConfigSetting == nil || data.ConfigSetting.StateArchivalSettings == nil {
		return 0, fmt.Errorf("stateArchival config entry has unexpected shape")
	}
	return uint32(data.ConfigSetting.StateArchivalSettings.MaxEntryTtl), nil
}

// item is one candidate ledger entry.
type item struct {
	label string
	key   xdr.LedgerKey
	b64   string
	// infra entries (instances, wasm, pair funds) are individually reported;
	// pool-account balance entries are aggregated in output.
	infra bool

	// resolved during classification:
	liveUntil uint32
	category  category
}

type category int

const (
	categoryMissing category = iota
	categoryArchived
	categoryLiveEnough
	categoryNeedsExtend
)

func (c category) String() string {
	switch c {
	case categoryMissing:
		return "missing"
	case categoryArchived:
		return "ARCHIVED"
	case categoryLiveEnough:
		return "live"
	case categoryNeedsExtend:
		return "extend"
	}
	return "?"
}

// Run enumerates the benchmark footprint, classifies each entry's TTL, and
// extends everything below the target. Returns an error if any entry is
// already archived (extend cannot resurrect it -- run restore or setup first)
// or if any extension transaction fails.
func Run(ctx context.Context, logger *log.Entry, st *state.State, opts Options) error {
	if opts.ExtendToLedgers == 0 {
		return fmt.Errorf("extend-to must be positive")
	}
	maxEntryTTL := uint32(MaxEntryTTLLedgers)
	if fetched, err := fetchNetworkMaxEntryTTL(ctx, st.RPCClient); err != nil {
		logger.WithError(err).Warnf("could not fetch the network's maxEntryTTL; assuming %d", maxEntryTTL)
	} else {
		maxEntryTTL = fetched
	}
	if clamped := clampExtendTo(opts.ExtendToLedgers, maxEntryTTL); clamped != opts.ExtendToLedgers {
		logger.Infof("extend-to %d exceeds the maximum extension %d (network maxEntryTTL %d - 1); clamping", opts.ExtendToLedgers, clamped, maxEntryTTL)
		opts.ExtendToLedgers = clamped
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}

	items, err := enumerate(st, opts.SkipBalances)
	if err != nil {
		return err
	}

	latest, err := st.RPCClient.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("getLatestLedger: %w", err)
	}

	items, err = resolveAndClassify(ctx, st.RPCClient, items, latest.Sequence, opts.ExtendToLedgers)
	if err != nil {
		return err
	}

	report(logger, items, latest.Sequence, opts)

	if opts.DryRun {
		return dryRunReport(ctx, logger, st, items, opts)
	}

	if opts.RestoreArchived {
		if err := restoreArchived(ctx, logger, st, items, opts); err != nil {
			return err
		}
	}

	archived := countCategory(items, categoryArchived)
	toExtend := filterCategory(items, categoryNeedsExtend)

	extended := 0
	for start := 0; start < len(toExtend); start += opts.BatchSize {
		end := min(start+opts.BatchSize, len(toExtend))
		keys := make([]xdr.LedgerKey, 0, end-start)
		for _, it := range toExtend[start:end] {
			keys = append(keys, it.key)
		}
		if err := state.SubmitExtendFootprintTTLAndWait(
			ctx, batchLogger(logger, "extend", toExtend[start:end], start, end, len(toExtend)),
			st.RPCClient, st.NetworkPassphrase, st.FeePayerKP, keys, opts.ExtendToLedgers,
		); err != nil {
			return fmt.Errorf("extend batch %d-%d of %d: %w", start, end-1, len(toExtend), err)
		}
		extended = end
		logger.Infof("extended %d/%d entries", extended, len(toExtend))
	}

	logger.Infof("extend-ttl complete: extended=%d already-live=%d missing=%d archived=%d",
		extended, countCategory(items, categoryLiveEnough), countCategory(items, categoryMissing), archived)
	return archivedError(items, archived)
}

// dryRunReport prints what would happen and simulates every would-be batch
// (extends, and restores when RestoreArchived is set) for a cost estimate.
// Nothing is submitted. Fails on archived entries unless RestoreArchived would
// handle them.
func dryRunReport(ctx context.Context, logger *log.Entry, st *state.State, items []item, opts Options) error {
	archivedItems := filterCategory(items, categoryArchived)
	toExtend := filterCategory(items, categoryNeedsExtend)

	logger.Infof("dry run: %d entries would be extended to %d ledgers (~%.1f days)",
		len(toExtend), opts.ExtendToLedgers, float64(opts.ExtendToLedgers)/LedgersPerDay)
	if err := estimateCost(ctx, logger, st, toExtend, opts); err != nil {
		return err
	}

	if opts.RestoreArchived && len(archivedItems) > 0 {
		logger.Infof("dry run: %d archived entries would be restored (RestoreFootprint, back to the ~120-day network minimum TTL)", len(archivedItems))
		if err := estimateRestoreCost(ctx, logger, st, archivedItems, opts); err != nil {
			return err
		}
		if opts.ExtendToLedgers > 120*LedgersPerDay {
			logger.Info("note: restored entries would additionally be topped up from ~120 days to the extend target; that top-up rent is not included in the estimates above")
		}
		return nil
	}
	return archivedError(items, len(archivedItems))
}

// restoreArchived submits batched RestoreFootprint transactions for every
// archived entry, then refetches and reclassifies those entries in place so
// the subsequent extend pass sees their post-restore TTLs.
func restoreArchived(ctx context.Context, logger *log.Entry, st *state.State, items []item, opts Options) error {
	archivedItems := filterCategory(items, categoryArchived)
	if len(archivedItems) == 0 {
		return nil
	}
	logger.Infof("restoring %d archived entries", len(archivedItems))

	restored := 0
	for start := 0; start < len(archivedItems); start += opts.BatchSize {
		end := min(start+opts.BatchSize, len(archivedItems))
		keys := make([]xdr.LedgerKey, 0, end-start)
		for _, it := range archivedItems[start:end] {
			keys = append(keys, it.key)
		}
		if err := state.SubmitRestoreFootprintAndWait(
			ctx, batchLogger(logger, "restore", archivedItems[start:end], start, end, len(archivedItems)),
			st.RPCClient, st.NetworkPassphrase, st.FeePayerKP, keys,
		); err != nil {
			return fmt.Errorf("restore batch %d-%d of %d: %w", start, end-1, len(archivedItems), err)
		}
		restored = end
		logger.Infof("restored %d/%d entries", restored, len(archivedItems))
	}

	// Reclassify the restored entries against a fresh latest ledger so the
	// extend pass tops them up (or skips them) based on their new TTLs.
	latest, err := st.RPCClient.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("getLatestLedger after restore: %w", err)
	}
	fetched, err := fetchLiveUntil(ctx, st.RPCClient, archivedItems)
	if err != nil {
		return fmt.Errorf("refetch restored entries: %w", err)
	}
	for i := range items {
		if items[i].category != categoryArchived {
			continue
		}
		f, ok := fetched[items[i].b64]
		switch {
		case !ok || f.data == nil:
			items[i].category = categoryMissing
		case f.liveUntil <= latest.Sequence:
			items[i].liveUntil = f.liveUntil
			// still archived -- the restore did not take; surfaced by
			// archivedError after the extend pass.
		case f.liveUntil-latest.Sequence >= effectiveTarget(opts.ExtendToLedgers):
			items[i].liveUntil = f.liveUntil
			items[i].category = categoryLiveEnough
		default:
			items[i].liveUntil = f.liveUntil
			items[i].category = categoryNeedsExtend
		}
	}
	return nil
}

// estimateRestoreCost simulates every would-be RestoreFootprint batch (without
// submitting) and reports the total.
func estimateRestoreCost(ctx context.Context, logger *log.Entry, st *state.State, archivedItems []item, opts Options) error {
	src, err := st.RPCClient.LoadAccount(ctx, st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("load fee payer for restore cost estimate: %w", err)
	}
	seq, err := src.GetSequenceNumber()
	if err != nil {
		return fmt.Errorf("get fee payer sequence: %w", err)
	}

	var totalResourceFee int64
	txCount := 0
	for start := 0; start < len(archivedItems); start += opts.BatchSize {
		end := min(start+opts.BatchSize, len(archivedItems))
		keys := make([]xdr.LedgerKey, 0, end-start)
		for _, it := range archivedItems[start:end] {
			keys = append(keys, it.key)
		}
		fee, err := state.SimulateRestoreFootprintFee(ctx, st.RPCClient, st.FeePayerKP.Address(), seq, keys)
		if err != nil {
			return fmt.Errorf("restore cost estimate: simulate batch %d-%d of %d: %w", start, end-1, len(archivedItems), err)
		}
		totalResourceFee += fee
		txCount++
		if txCount%20 == 0 {
			logger.Infof("restore cost estimate: simulated %d/%d entries", end, len(archivedItems))
		}
	}

	inclusionTotal := int64(txCount) * state.InclusionFee
	estimated := totalResourceFee + inclusionTotal
	paddedMax := int64(float64(totalResourceFee)*state.SetupResourcePadFactor) + inclusionTotal
	logger.Infof("restore cost estimate: %d txns, resource fees %s XLM + inclusion %s XLM = ~%s XLM (max bid with %.2fx pad: %s XLM)",
		txCount, formatXLM(totalResourceFee), formatXLM(inclusionTotal), formatXLM(estimated),
		state.SetupResourcePadFactor, formatXLM(paddedMax))
	return nil
}

// estimateCost simulates every would-be extension batch (without submitting)
// and reports the total cost. Simulation returns the same rent-dominated
// resource fee a real submission would carry, so the estimate is the RPC's own
// fee math rather than a heuristic; only the sequential batching differs from
// a real run. Signatures are not needed for simulation, so a dry run stays
// side-effect-free.
func estimateCost(ctx context.Context, logger *log.Entry, st *state.State, toExtend []item, opts Options) error {
	if len(toExtend) == 0 {
		return nil
	}
	src, err := st.RPCClient.LoadAccount(ctx, st.FeePayerKP.Address())
	if err != nil {
		return fmt.Errorf("load fee payer for cost estimate: %w", err)
	}
	seq, err := src.GetSequenceNumber()
	if err != nil {
		return fmt.Errorf("get fee payer sequence: %w", err)
	}

	var totalResourceFee int64
	txCount := 0
	for start := 0; start < len(toExtend); start += opts.BatchSize {
		end := min(start+opts.BatchSize, len(toExtend))
		keys := make([]xdr.LedgerKey, 0, end-start)
		for _, it := range toExtend[start:end] {
			keys = append(keys, it.key)
		}
		fee, err := state.SimulateExtendFootprintTTLFee(
			ctx, st.RPCClient, st.FeePayerKP.Address(), seq, keys, opts.ExtendToLedgers)
		if err != nil {
			return fmt.Errorf("cost estimate: simulate batch %d-%d of %d: %w", start, end-1, len(toExtend), err)
		}
		totalResourceFee += fee
		txCount++
		if txCount%20 == 0 {
			logger.Infof("cost estimate: simulated %d/%d entries", end, len(toExtend))
		}
	}

	inclusionTotal := int64(txCount) * state.InclusionFee
	estimated := totalResourceFee + inclusionTotal
	// Real submissions pad resources by the setup pad factor; the refundable
	// rent portion is returned when unused, so the padded figure is the max
	// bid, not the expected charge.
	paddedMax := int64(float64(totalResourceFee)*state.SetupResourcePadFactor) + inclusionTotal
	logger.Infof("cost estimate: %d txns, resource fees %s XLM + inclusion %s XLM = ~%s XLM (max bid with %.2fx pad: %s XLM)",
		txCount, formatXLM(totalResourceFee), formatXLM(inclusionTotal), formatXLM(estimated),
		state.SetupResourcePadFactor, formatXLM(paddedMax))
	return nil
}

// formatXLM renders stroops as an exact 7-decimal XLM amount.
func formatXLM(stroops int64) string {
	neg := ""
	if stroops < 0 {
		neg = "-"
		stroops = -stroops
	}
	return fmt.Sprintf("%s%d.%07d", neg, stroops/10_000_000, stroops%10_000_000)
}

// enumerate builds the full deterministic candidate set from state: contract
// instances, pair fund balance entries, and per-account OZ balance entries.
// Wasm code entries are appended later, once instance entries reveal their
// hashes.
func enumerate(st *state.State, skipBalances bool) ([]item, error) {
	var items []item

	addInstance := func(label, contractIDStr string) error {
		if contractIDStr == "" {
			return nil
		}
		contractID, err := ledger.DecodeContractID(contractIDStr)
		if err != nil {
			return fmt.Errorf("%s: decode contract ID %q: %w", label, contractIDStr, err)
		}
		items = append(items, item{label: label, key: ledger.ContractInstanceLedgerKey(contractID), infra: true})
		return nil
	}

	if err := addInstance("oz-token instance", st.OZTokenContract); err != nil {
		return nil, err
	}
	if err := addInstance("soroswap-router instance", st.SoroswapRouterContract); err != nil {
		return nil, err
	}
	if err := addInstance("soroswap-factory instance", st.SoroswapFactoryContract); err != nil {
		return nil, err
	}
	for i, sac := range st.SACs {
		if err := addInstance(fmt.Sprintf("sac[%d] instance", i), sac); err != nil {
			return nil, err
		}
	}
	for i, pair := range st.SoroswapPairContracts {
		if err := addInstance(fmt.Sprintf("pair[%d] instance", i), pair); err != nil {
			return nil, err
		}
	}

	// Pair SAC fund balances: each pair holds balances in the SACs of its two
	// pooled assets. Which SAC pairs with which pool is not recorded in state,
	// so probe every combination; non-existent combos classify as missing,
	// which is expected and harmless.
	for pi, pair := range st.SoroswapPairContracts {
		if pair == "" {
			continue
		}
		pairID, err := ledger.DecodeContractID(pair)
		if err != nil {
			return nil, fmt.Errorf("pair[%d]: decode contract ID: %w", pi, err)
		}
		holder := ledger.ContractScAddress(pairID)
		for si, sac := range st.SACs {
			if sac == "" {
				continue
			}
			sacID, err := ledger.DecodeContractID(sac)
			if err != nil {
				return nil, fmt.Errorf("sac[%d]: decode contract ID: %w", si, err)
			}
			items = append(items, item{
				label: fmt.Sprintf("pair[%d] funds in sac[%d]", pi, si),
				key:   ledger.ContractBalanceLedgerKey(sacID, holder),
				infra: true,
			})
		}
	}

	// OZ balance entries for every derived pool account. SAC holders are a
	// subset of AccountKPs and pool-account SAC balances are classic
	// trustlines (no TTL), so this is the complete per-account set.
	if st.OZTokenContract != "" && !skipBalances {
		ozID, err := ledger.DecodeContractID(st.OZTokenContract)
		if err != nil {
			return nil, fmt.Errorf("oz token: decode contract ID: %w", err)
		}
		for _, kp := range st.AccountKPs {
			key, err := ledger.OZBalanceLedgerKey(ozID, kp.Address())
			if err != nil {
				return nil, fmt.Errorf("oz balance key for %s: %w", kp.Address(), err)
			}
			items = append(items, item{label: "oz balance " + kp.Address(), key: key})
		}
	}

	return items, nil
}

// resolveAndClassify fetches every candidate's current entry, discovers wasm
// code entries from the fetched instances, and classifies all of them against
// the latest ledger and the extend target.
func resolveAndClassify(
	ctx context.Context,
	rpc ledger.LedgerEntriesClient,
	items []item,
	latestLedger uint32,
	extendTo uint32,
) ([]item, error) {
	fetched, err := fetchLiveUntil(ctx, rpc, items)
	if err != nil {
		return nil, err
	}

	// Discover wasm code entries from instance results (deduped by hash; SAC
	// instances are native executables and contribute none).
	seenWasm := map[xdr.Hash]bool{}
	var wasmItems []item
	for i := range items {
		f, ok := fetched[items[i].b64]
		if !ok || f.data == nil {
			continue
		}
		if h := ledger.WasmHashFromInstance(f.data); h != nil && !seenWasm[*h] {
			seenWasm[*h] = true
			wasmItems = append(wasmItems, item{
				label: fmt.Sprintf("wasm %x (from %s)", (*h)[:4], items[i].label),
				key:   ledger.ContractCodeLedgerKey(*h),
				infra: true,
			})
		}
	}
	if len(wasmItems) > 0 {
		wasmFetched, err := fetchLiveUntil(ctx, rpc, wasmItems)
		if err != nil {
			return nil, err
		}
		for k, v := range wasmFetched {
			fetched[k] = v
		}
		items = append(items, wasmItems...)
	}

	for i := range items {
		f, ok := fetched[items[i].b64]
		switch {
		case !ok || f.data == nil:
			items[i].category = categoryMissing
		case f.liveUntil <= latestLedger:
			items[i].liveUntil = f.liveUntil
			items[i].category = categoryArchived
		case f.liveUntil-latestLedger >= effectiveTarget(extendTo):
			items[i].liveUntil = f.liveUntil
			items[i].category = categoryLiveEnough
		default:
			items[i].liveUntil = f.liveUntil
			items[i].category = categoryNeedsExtend
		}
	}
	return items, nil
}

type fetchedEntry struct {
	data      *xdr.LedgerEntryData
	liveUntil uint32
}

// fetchLiveUntil marshals each item's key (memoizing the base64 form on the
// item) and resolves the current entry + liveUntilLedgerSeq for those that
// exist.
func fetchLiveUntil(ctx context.Context, rpc ledger.LedgerEntriesClient, items []item) (map[string]fetchedEntry, error) {
	keys := make([]string, len(items))
	for i := range items {
		b64, err := xdr.MarshalBase64(items[i].key)
		if err != nil {
			return nil, fmt.Errorf("marshal key for %s: %w", items[i].label, err)
		}
		items[i].b64 = b64
		keys[i] = b64
	}
	entries, err := ledger.FetchLedgerEntriesByKey(ctx, rpc, keys, ledger.DefaultBatchSize)
	if err != nil {
		return nil, fmt.Errorf("getLedgerEntries: %w", err)
	}
	fetched := make(map[string]fetchedEntry, len(entries))
	for keyB64, entry := range entries {
		var data xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(entry.DataXDR, &data); err != nil {
			return nil, fmt.Errorf("decode entry %s: %w", keyB64, err)
		}
		var liveUntil uint32
		if entry.LiveUntilLedgerSeq != nil {
			liveUntil = *entry.LiveUntilLedgerSeq
		}
		fetched[keyB64] = fetchedEntry{data: &data, liveUntil: liveUntil}
	}
	return fetched, nil
}

// report logs every infra entry individually and pool-account balances as an
// aggregate, so the output stays readable with thousands of accounts.
func report(logger *log.Entry, items []item, latestLedger uint32, opts Options) {
	for _, it := range items {
		if !it.infra {
			continue
		}
		remaining := "-"
		days := "-"
		if it.liveUntil > 0 {
			r := int64(it.liveUntil) - int64(latestLedger)
			remaining = fmt.Sprintf("%d", r)
			days = fmt.Sprintf("%.1f", float64(r)/LedgersPerDay)
		}
		logger.Infof("%-40s liveUntil=%-11d remaining=%-9s ~days=%-6s %s",
			it.label, it.liveUntil, remaining, days, it.category)
	}

	// Aggregate the non-infra (pool balance) entries per category.
	counts := map[category]int{}
	var minRemaining, maxRemaining int64
	first := true
	for _, it := range items {
		if it.infra {
			continue
		}
		counts[it.category]++
		if it.liveUntil == 0 {
			continue
		}
		r := int64(it.liveUntil) - int64(latestLedger)
		if first || r < minRemaining {
			minRemaining = r
		}
		if first || r > maxRemaining {
			maxRemaining = r
		}
		first = false
	}
	if total := counts[categoryMissing] + counts[categoryArchived] + counts[categoryLiveEnough] + counts[categoryNeedsExtend]; total > 0 {
		logger.Infof("pool oz balances: total=%d extend=%d live=%d missing=%d archived=%d remaining=[%.1fd, %.1fd]",
			total, counts[categoryNeedsExtend], counts[categoryLiveEnough],
			counts[categoryMissing], counts[categoryArchived],
			float64(minRemaining)/LedgersPerDay, float64(maxRemaining)/LedgersPerDay)
	}
}

// archivedError returns nil when no entries are archived; otherwise an error
// naming up to maxListedPerCategory of them. Archived entries cannot be
// extended -- they must be restored (restore subcommand) or recreated (setup).
func archivedError(items []item, archived int) error {
	if archived == 0 {
		return nil
	}
	var names []string
	for _, it := range items {
		if it.category != categoryArchived {
			continue
		}
		if len(names) < maxListedPerCategory {
			names = append(names, it.label)
		}
	}
	sort.Strings(names)
	suffix := ""
	if archived > len(names) {
		suffix = fmt.Sprintf(" (and %d more)", archived-len(names))
	}
	return fmt.Errorf(
		"%d entries are already archived and cannot be extended -- re-run with --restore-archived to submit RestoreFootprint transactions for them: %s%s",
		archived, strings.Join(names, ", "), suffix)
}

func countCategory(items []item, c category) int {
	n := 0
	for _, it := range items {
		if it.category == c {
			n++
		}
	}
	return n
}

func filterCategory(items []item, c category) []item {
	var out []item
	for _, it := range items {
		if it.category == c {
			out = append(out, it)
		}
	}
	return out
}
