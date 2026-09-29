// Command ttl-probe reports the archival status of a state file's Soroban
// footprint: contract instances, their wasm code entries, and the SAC
// contract-held balance entries of the soroswap pairs. For each entry it
// prints liveUntilLedgerSeq, the remaining ledgers, and an approximate
// days-of-runway figure (5s ledgers). Entries whose TTL has already elapsed
// are marked ARCHIVED; keys missing from live state are marked EVICTED/absent.
//
// Usage:
//
//	ttl-probe [--rpc-url URL] <state-file>
//
// No secrets are required: only public contract IDs from the state file are
// probed. Pass additional addresses with --address to also check their OZ
// token balance entries (e.g. addresses recovered from a trace).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	queryTimeout    = 60 * time.Second
	ledgersPerDay   = 17_280 // 5s ledgers
	maxKeysPerQuery = 200
)

type probe struct {
	label string
	key   xdr.LedgerKey
}

func main() {
	rpcURL := flag.String("rpc-url", "", "RPC URL (overrides the state file's)")
	var extraAddrs stringList
	flag.Var(&extraAddrs, "address", "Additional G... address whose OZ balance entry should be probed (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [--rpc-url URL] [--address G...] <state-file>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*rpcURL, flag.Arg(0), extraAddrs); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func run(rpcURL, stateFile string, extraAddrs []string) error {
	ps, err := state.NewPersistedState(stateFile)
	if err != nil {
		return fmt.Errorf("read state file: %w", err)
	}
	if rpcURL == "" {
		rpcURL = ps.RPCURL
	}
	client := rpcclient.NewClient(rpcURL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	latest, err := client.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("getLatestLedger: %w", err)
	}
	fmt.Printf("rpc:            %s\n", rpcURL)
	fmt.Printf("latest ledger:  %d\n\n", latest.Sequence)

	// Round 1: instance entries for every known contract.
	contracts := []struct{ label, id string }{
		{"oz-token instance", ps.OZTokenContract},
		{"soroswap-router instance", ps.SoroswapRouterContract},
		{"soroswap-factory instance", ps.SoroswapFactoryContract},
	}
	for i, sac := range ps.SACs {
		contracts = append(contracts, struct{ label, id string }{fmt.Sprintf("sac[%d] instance", i), sac})
	}
	for i, pair := range ps.SoroswapPairContracts {
		contracts = append(contracts, struct{ label, id string }{fmt.Sprintf("pair[%d] instance", i), pair})
	}

	var probes []probe
	for _, c := range contracts {
		if c.id == "" {
			continue
		}
		contractID, err := ledger.DecodeContractID(c.id)
		if err != nil {
			return fmt.Errorf("%s: %w", c.label, err)
		}
		probes = append(probes, probe{c.label, ledger.ContractInstanceLedgerKey(contractID)})
	}

	// SAC balance entries held by each pair (pair funds), tried under every
	// SAC; only existing combinations show up in the results.
	for pi, pair := range ps.SoroswapPairContracts {
		for si, sac := range ps.SACs {
			sacID, err := ledger.DecodeContractID(sac)
			if err != nil {
				return fmt.Errorf("sac[%d]: %w", si, err)
			}
			pairID, err := ledger.DecodeContractID(pair)
			if err != nil {
				return fmt.Errorf("pair[%d]: %w", pi, err)
			}
			probes = append(probes, probe{
				fmt.Sprintf("pair[%d] funds in sac[%d]", pi, si),
				ledger.ContractBalanceLedgerKey(sacID, ledger.ContractScAddress(pairID)),
			})
		}
	}

	// OZ balance entries for explicitly provided addresses.
	for _, addr := range extraAddrs {
		ozID, err := ledger.DecodeContractID(ps.OZTokenContract)
		if err != nil {
			return fmt.Errorf("oz token: %w", err)
		}
		key, err := ledger.OZBalanceLedgerKey(ozID, addr)
		if err != nil {
			return fmt.Errorf("oz balance for %s: %w", addr, err)
		}
		probes = append(probes, probe{fmt.Sprintf("oz balance %s..%s", addr[:4], addr[len(addr)-4:]), key})
	}

	results, err := fetchEntries(ctx, client, probes)
	if err != nil {
		return err
	}

	// Round 2: wasm code entries discovered from the instance results.
	var wasmProbes []probe
	seenWasm := map[xdr.Hash]bool{}
	for _, p := range probes {
		r, ok := results[p.label]
		if !ok || r.entry == nil {
			continue
		}
		if h := ledger.WasmHashFromInstance(r.entry); h != nil && !seenWasm[*h] {
			seenWasm[*h] = true
			wasmProbes = append(wasmProbes, probe{
				fmt.Sprintf("wasm %x (from %s)", (*h)[:4], p.label),
				ledger.ContractCodeLedgerKey(*h),
			})
		}
	}
	if len(wasmProbes) > 0 {
		wasmResults, err := fetchEntries(ctx, client, wasmProbes)
		if err != nil {
			return err
		}
		for k, v := range wasmResults {
			results[k] = v
		}
		probes = append(probes, wasmProbes...)
	}

	fmt.Printf("%-38s %14s %12s %10s  %s\n", "entry", "liveUntil", "remaining", "~days", "status")
	fmt.Println(strings.Repeat("-", 92))
	for _, p := range probes {
		r, ok := results[p.label]
		if !ok || r.entry == nil {
			fmt.Printf("%-38s %14s %12s %10s  %s\n", p.label, "-", "-", "-", "EVICTED or never existed")
			continue
		}
		if r.liveUntil == 0 {
			// Classic entries (trustlines, accounts) have no TTL.
			fmt.Printf("%-38s %14s %12s %10s  %s\n", p.label, "-", "-", "-", "live (no TTL)")
			continue
		}
		remaining := int64(r.liveUntil) - int64(latest.Sequence)
		status := "LIVE"
		if remaining < 0 {
			status = "ARCHIVED (expired, awaiting eviction)"
		} else if remaining < 7*ledgersPerDay {
			status = "LIVE (expiring soon!)"
		}
		fmt.Printf("%-38s %14d %12d %10.1f  %s\n",
			p.label, r.liveUntil, remaining, float64(remaining)/ledgersPerDay, status)
	}
	return nil
}

type entryResult struct {
	entry     *xdr.LedgerEntryData
	liveUntil uint32
}

// fetchEntries resolves each probe's ledger entry and liveUntilLedgerSeq,
// keyed by probe label.
func fetchEntries(ctx context.Context, client *rpcclient.Client, probes []probe) (map[string]entryResult, error) {
	results := make(map[string]entryResult, len(probes))
	labelByKey := make(map[string]string, len(probes))
	var keys []string
	for _, p := range probes {
		b64, err := xdr.MarshalBase64(p.key)
		if err != nil {
			return nil, fmt.Errorf("marshal key for %s: %w", p.label, err)
		}
		labelByKey[b64] = p.label
		keys = append(keys, b64)
	}
	for start := 0; start < len(keys); start += maxKeysPerQuery {
		end := min(start+maxKeysPerQuery, len(keys))
		resp, err := client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys[start:end]})
		if err != nil {
			return nil, fmt.Errorf("getLedgerEntries: %w", err)
		}
		for _, e := range resp.Entries {
			label, ok := labelByKey[e.KeyXDR]
			if !ok {
				continue
			}
			var data xdr.LedgerEntryData
			if err := xdr.SafeUnmarshalBase64(e.DataXDR, &data); err != nil {
				return nil, fmt.Errorf("decode entry for %s: %w", label, err)
			}
			var liveUntil uint32
			if e.LiveUntilLedgerSeq != nil {
				liveUntil = *e.LiveUntilLedgerSeq
			}
			results[label] = entryResult{entry: &data, liveUntil: liveUntil}
		}
	}
	return results, nil
}
