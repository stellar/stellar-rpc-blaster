// Command pool-xlm reports native XLM held across a state file's participant
// account pool (and the fee payer, separately).
//
// Usage:
//
//	FEE_PAYER=S... pool-xlm [flags] <state-file>
//
// By default it prints pool/fee-payer totals plus a "free balance"
// distribution: each account's spendable XLM after its minimum reserve
// (base-reserve * (2 + subentries + sponsoring - sponsored)) and selling
// liabilities. Accounts whose free balance is below --min-free-xlm are
// flagged -- these can no longer make the dust transfers the benchmark's
// simple-payment workload relies on (the payment amount comes out of the
// child account; fees are covered by the fee-bump, so only free balance
// matters). Use --per-account to dump every account.
//
// The fee-payer seed is read from FEE_PAYER and validated against the hash in
// the state file (same check the other commands use); accounts are re-derived
// from the stored indices. Balances are read in batched getLedgerEntries
// requests rather than one LoadAccount per account.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	stroopsPerXLM      = 10_000_000
	maxLedgerEntryKeys = 200
	chunkConcurrency   = 16
	queryTimeout       = 2 * time.Minute

	// defaultBaseReserveStroops is the protocol base reserve (0.5 XLM). The
	// minimum balance is this times (2 + numSubEntries + numSponsoring -
	// numSponsored). Overridable via --base-reserve-xlm in case a target
	// network runs a non-default value.
	defaultBaseReserveStroops = stroopsPerXLM / 2

	// dustTransferStroops is the benchmark simple-payment amount (0.0000001
	// XLM == 1 stroop, see benchmark.simplePaymentAmount). An account needs at
	// least this much free balance to make one payment. It is the default
	// --min-free-xlm threshold.
	dustTransferStroops = 1

	// maxFlaggedListed caps how many low accounts are printed by address so a
	// widespread shortfall does not scroll thousands of lines.
	maxFlaggedListed = 50
)

// accountInfo is a decoded account entry's balance and derived reserve/free.
type accountInfo struct {
	balance     int64
	minReserve  int64
	freeBalance int64 // balance - minReserve - sellingLiabilities, floored at nothing
}

func main() {
	rpcURL := flag.String("rpc-url", "", "Override the RPC URL stored in the state file")
	perAccount := flag.Bool("per-account", false, "Print one line per pool account (address, balance, reserve, free)")
	baseReserveXLM := flag.Float64("base-reserve-xlm", float64(defaultBaseReserveStroops)/stroopsPerXLM,
		"Protocol base reserve in XLM used to compute each account's minimum balance")
	minFreeXLM := flag.Float64("min-free-xlm", float64(dustTransferStroops)/stroopsPerXLM,
		"Flag accounts whose free (spendable) balance is below this many XLM")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: FEE_PAYER=S... %s [flags] <state-file>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	opts := reportOptions{
		perAccount:       *perAccount,
		baseReserve:      xlmToStroops(*baseReserveXLM),
		minFreeThreshold: xlmToStroops(*minFreeXLM),
	}
	if err := run(os.Getenv("FEE_PAYER"), *rpcURL, flag.Arg(0), opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type reportOptions struct {
	perAccount       bool
	baseReserve      int64
	minFreeThreshold int64
}

func run(feePayerSeed, rpcURL, stateFile string, opts reportOptions) error {
	if feePayerSeed == "" {
		return fmt.Errorf("FEE_PAYER environment variable is required")
	}

	ps, err := state.NewPersistedState(stateFile)
	if err != nil {
		return fmt.Errorf("read state file: %w", err)
	}
	st, err := state.FromPersistedState(ps, feePayerSeed, rpcURL)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	effectiveRPC := rpcURL
	if effectiveRPC == "" {
		effectiveRPC = ps.RPCURL
	}

	// Collect every address: participant pool plus the fee payer.
	poolAddrs := make([]string, len(st.AccountKPs))
	for i, kp := range st.AccountKPs {
		poolAddrs[i] = kp.Address()
	}
	feePayerAddr := st.FeePayerKP.Address()

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	infos, err := loadAccountInfoByAddress(ctx, st, append(poolAddrs, feePayerAddr), opts.baseReserve)
	if err != nil {
		return err
	}

	var poolStroops int64
	found, missing := 0, 0
	frees := make([]int64, 0, len(poolAddrs))
	type flagged struct {
		addr string
		info accountInfo
	}
	var low []flagged
	for _, addr := range poolAddrs {
		info, ok := infos[addr]
		if !ok {
			missing++
			continue
		}
		found++
		poolStroops += info.balance
		frees = append(frees, info.freeBalance)
		if info.freeBalance < opts.minFreeThreshold {
			low = append(low, flagged{addr, info})
		}
	}
	feePayer, feePayerFound := infos[feePayerAddr]

	fmt.Printf("state file:    %s\n", stateFile)
	fmt.Printf("rpc:           %s\n", effectiveRPC)
	fmt.Printf("base reserve:  %s XLM/subentry\n", formatXLM(opts.baseReserve))
	fmt.Printf("min-free flag: %s XLM (dust transfer = %s XLM)\n",
		formatXLM(opts.minFreeThreshold), formatXLM(dustTransferStroops))
	fmt.Println()
	fmt.Printf("pool accounts: %d (found %d, missing %d)\n", len(poolAddrs), found, missing)
	fmt.Printf("pool total:    %s XLM (%d stroops)\n", formatXLM(poolStroops), poolStroops)

	if len(frees) > 0 {
		sort.Slice(frees, func(i, j int) bool { return frees[i] < frees[j] })
		fmt.Printf("free balance:  min=%s  p1=%s  p50=%s  p99=%s  max=%s XLM\n",
			formatXLM(frees[0]),
			formatXLM(percentile(frees, 0.01)),
			formatXLM(percentile(frees, 0.50)),
			formatXLM(percentile(frees, 0.99)),
			formatXLM(frees[len(frees)-1]))
	}

	if missing > 0 {
		fmt.Printf("WARNING: %d pool account(s) not found on chain -- cannot transfer at all\n", missing)
	}
	if len(low) == 0 {
		fmt.Printf("low accounts:  0 below %s XLM free -- all can still make dust transfers\n",
			formatXLM(opts.minFreeThreshold))
	} else {
		sort.Slice(low, func(i, j int) bool { return low[i].info.freeBalance < low[j].info.freeBalance })
		fmt.Printf("low accounts:  %d below %s XLM free (spendable exhausted or near reserve floor):\n",
			len(low), formatXLM(opts.minFreeThreshold))
		for i, f := range low {
			if i >= maxFlaggedListed {
				fmt.Printf("  ... and %d more\n", len(low)-maxFlaggedListed)
				break
			}
			fmt.Printf("  %s  balance=%s  reserve=%s  free=%s XLM\n",
				f.addr, formatXLM(f.info.balance), formatXLM(f.info.minReserve), formatXLM(f.info.freeBalance))
		}
	}

	if opts.perAccount {
		fmt.Println("\nper-account (address, balance, reserve, free XLM):")
		for _, addr := range poolAddrs {
			info, ok := infos[addr]
			if !ok {
				fmt.Printf("  %s  MISSING\n", addr)
				continue
			}
			fmt.Printf("  %s  %s  %s  %s\n",
				addr, formatXLM(info.balance), formatXLM(info.minReserve), formatXLM(info.freeBalance))
		}
	}

	fmt.Println()
	if feePayerFound {
		fmt.Printf("fee payer %s: %s XLM (%d stroops)\n", feePayerAddr, formatXLM(feePayer.balance), feePayer.balance)
	} else {
		fmt.Printf("fee payer %s: NOT FOUND on chain\n", feePayerAddr)
	}
	fmt.Printf("grand total:   %s XLM (%d stroops)\n", formatXLM(poolStroops+feePayer.balance), poolStroops+feePayer.balance)
	return nil
}

// loadAccountInfoByAddress returns each account's balance and derived reserve /
// free balance keyed by address. Addresses with no account entry on chain are
// absent from the map. Keys are chunked and fetched concurrently.
func loadAccountInfoByAddress(ctx context.Context, st *state.State, addresses []string, baseReserve int64) (map[string]accountInfo, error) {
	keys := make([]string, 0, len(addresses))
	addressByKey := make(map[string]string, len(addresses))
	for _, addr := range addresses {
		key, err := accountLedgerKey(addr)
		if err != nil {
			return nil, fmt.Errorf("build ledger key for %s: %w", addr, err)
		}
		if _, seen := addressByKey[key]; seen {
			continue
		}
		addressByKey[key] = addr
		keys = append(keys, key)
	}

	chunks := chunkStrings(keys, maxLedgerEntryKeys)
	sem := make(chan struct{}, chunkConcurrency)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		result = make(map[string]accountInfo, len(addresses))
		errs   = make([]error, len(chunks))
	)

	for ci, chunk := range chunks {
		ci, chunk := ci, chunk
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			resp, err := st.RPCClient.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: chunk})
			if err != nil {
				errs[ci] = err
				return
			}
			for _, entry := range resp.Entries {
				addr, ok := addressByKey[entry.KeyXDR]
				if !ok {
					continue
				}
				info, err := accountInfoFromEntryXDR(entry.DataXDR, baseReserve)
				if err != nil {
					errs[ci] = fmt.Errorf("account %s: %w", addr, err)
					return
				}
				mu.Lock()
				result[addr] = info
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("getLedgerEntries: %w", err)
		}
	}
	return result, nil
}

func accountLedgerKey(address string) (string, error) {
	accountID, err := xdr.AddressToAccountId(address)
	if err != nil {
		return "", err
	}
	lk, err := accountID.LedgerKey()
	if err != nil {
		return "", err
	}
	return xdr.MarshalBase64(lk)
}

// accountInfoFromEntryXDR decodes an account entry and derives its minimum
// reserve and free (spendable) balance. Minimum balance follows the protocol
// formula: baseReserve * (2 + numSubEntries + numSponsoring - numSponsored);
// free balance additionally subtracts native selling liabilities.
func accountInfoFromEntryXDR(dataXDR string, baseReserve int64) (accountInfo, error) {
	if dataXDR == "" {
		return accountInfo{}, fmt.Errorf("empty ledger entry data")
	}
	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(dataXDR, &entry); err != nil {
		return accountInfo{}, fmt.Errorf("decode ledger entry: %w", err)
	}
	if entry.Type != xdr.LedgerEntryTypeAccount || entry.Account == nil {
		return accountInfo{}, fmt.Errorf("ledger entry is not an account entry")
	}
	acct := entry.Account

	subEntries := int64(acct.NumSubEntries)
	var sellingLiabilities, numSponsoring, numSponsored int64
	if acct.Ext.V == 1 && acct.Ext.V1 != nil {
		sellingLiabilities = int64(acct.Ext.V1.Liabilities.Selling)
		if acct.Ext.V1.Ext.V == 2 && acct.Ext.V1.Ext.V2 != nil {
			numSponsoring = int64(acct.Ext.V1.Ext.V2.NumSponsoring)
			numSponsored = int64(acct.Ext.V1.Ext.V2.NumSponsored)
		}
	}

	minReserve := baseReserve * (2 + subEntries + numSponsoring - numSponsored)
	return accountInfo{
		balance:     int64(acct.Balance),
		minReserve:  minReserve,
		freeBalance: int64(acct.Balance) - minReserve - sellingLiabilities,
	}, nil
}

func chunkStrings(items []string, size int) [][]string {
	if size <= 0 {
		return [][]string{items}
	}
	var chunks [][]string
	for start := 0; start < len(items); start += size {
		end := min(start+size, len(items))
		chunks = append(chunks, items[start:end])
	}
	return chunks
}

// percentile returns the value at fraction p (0..1) of an ascending-sorted
// slice using nearest-rank.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func xlmToStroops(x float64) int64 {
	// Round to the nearest stroop; inputs are small, exactness is not critical
	// for threshold/reserve knobs.
	if x < 0 {
		return -int64(-x*stroopsPerXLM + 0.5)
	}
	return int64(x*stroopsPerXLM + 0.5)
}

// formatXLM renders a stroop amount as an exact XLM decimal (7 places) without
// floating-point rounding.
func formatXLM(stroops int64) string {
	neg := stroops < 0
	if neg {
		stroops = -stroops
	}
	whole := stroops / stroopsPerXLM
	frac := stroops % stroopsPerXLM
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%07d", sign, whole, frac)
}
