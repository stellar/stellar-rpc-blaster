package benchmark

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// maxLedgerEntryKeys bounds the number of keys sent in a single getLedgerEntries
// request. stellar-rpc caps keys per request (default 200); larger account sets
// are split into this many keys per round-trip.
const maxLedgerEntryKeys = 200

// sequenceLoadChunkConcurrency bounds how many getLedgerEntries chunks are
// in flight at once. Kept modest so prehydration does not flood the RPC.
const sequenceLoadChunkConcurrency = 16

type sequenceManager struct {
	counters []atomic.Int64
}

func newSequenceManager(ctx context.Context, st *state.State, accounts []*keypair.Full, label string) (*sequenceManager, error) {
	seqBase, err := loadSequenceNumbers(ctx, st, accounts)
	if err != nil {
		return nil, fmt.Errorf("load %s sequence numbers: %w", label, err)
	}
	manager := &sequenceManager{counters: make([]atomic.Int64, len(accounts))}
	for i, base := range seqBase {
		manager.counters[i].Store(base)
	}
	return manager, nil
}

func (m *sequenceManager) Next(index int) int64 {
	return m.counters[index].Add(1)
}

func (m *sequenceManager) Reset(jsonRPCID int64) {
	if len(m.counters) == 0 || jsonRPCID <= 0 {
		return
	}
	index := int((jsonRPCID - 1) % int64(len(m.counters)))
	m.counters[index].Add(-1)
}

func loadSequenceNumbers(ctx context.Context, st *state.State, accountKPs []*keypair.Full) ([]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	addresses := make([]string, len(accountKPs))
	for i, kp := range accountKPs {
		addresses[i] = kp.Address()
	}

	seqByAddress, err := loadSequenceNumbersByAddress(ctx, st.RPCClient, addresses)
	if err != nil {
		return nil, err
	}

	seqNums := make([]int64, len(accountKPs))
	for i, addr := range addresses {
		seq, ok := seqByAddress[addr]
		if !ok {
			return nil, fmt.Errorf("account[%d] %s: no ledger entry returned", i, addr)
		}
		seqNums[i] = seq
	}
	return seqNums, nil
}

// loadSequenceNumbersByAddress returns each account's current on-chain sequence
// number keyed by address. It batches reads through getLedgerEntries -- which
// accepts many keys per request -- so a large account set resolves in a handful
// of round-trips instead of one LoadAccount call each. Keys are chunked to
// maxLedgerEntryKeys and chunks are fetched concurrently. Addresses with no
// ledger entry are simply absent from the returned map; callers decide whether
// that is an error. Duplicate addresses collapse to a single key.
func loadSequenceNumbersByAddress(ctx context.Context, rpc *rpcclient.Client, addresses []string) (map[string]int64, error) {
	if rpc == nil {
		return nil, fmt.Errorf("missing RPC client")
	}

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
	sem := make(chan struct{}, sequenceLoadChunkConcurrency)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		result = make(map[string]int64, len(addresses))
		errs   = make([]error, len(chunks))
	)

	for ci, chunk := range chunks {
		ci, chunk := ci, chunk
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: chunk})
			if err != nil {
				errs[ci] = err
				return
			}
			for _, entry := range resp.Entries {
				addr, ok := addressByKey[entry.KeyXDR]
				if !ok {
					continue
				}
				seq, err := accountSeqNumFromEntryXDR(entry.DataXDR)
				if err != nil {
					errs[ci] = fmt.Errorf("account %s: %w", addr, err)
					return
				}
				mu.Lock()
				result[addr] = seq
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("getLedgerEntries: %w", err)
		}
	}
	return result, nil
}

// accountLedgerKey returns the base64-encoded LedgerKey for an account address,
// matching the encoding stellar-rpc echoes back in LedgerEntryResult.KeyXDR.
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

// accountSeqNumFromEntryXDR extracts the sequence number from a base64-encoded
// account LedgerEntryData.
func accountSeqNumFromEntryXDR(dataXDR string) (int64, error) {
	if dataXDR == "" {
		return 0, fmt.Errorf("empty ledger entry data")
	}
	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(dataXDR, &entry); err != nil {
		return 0, fmt.Errorf("decode ledger entry: %w", err)
	}
	if entry.Type != xdr.LedgerEntryTypeAccount || entry.Account == nil {
		return 0, fmt.Errorf("ledger entry is not an account entry")
	}
	return int64(entry.Account.SeqNum), nil
}

// chunkStrings splits items into consecutive slices of at most size elements.
func chunkStrings(items []string, size int) [][]string {
	if size <= 0 {
		size = len(items)
	}
	var chunks [][]string
	for start := 0; start < len(items); start += size {
		chunks = append(chunks, items[start:min(start+size, len(items))])
	}
	return chunks
}
