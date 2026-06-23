package benchmark

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

type transactionPollClient interface {
	GetTransactions(ctx context.Context, requests []transactionPollRequest) ([]transactionPollAttemptResult, error)
}

// latestLedgerObserver fetches the current network ledger sequence and its
// close time. The poll scheduler's ledger-advance gate uses this only as a
// fallback to keep the shared ledger clock advancing when poll traffic dies
// down (the drain tail); in steady state the clock is kept fresh for free from
// getTransaction responses. A poll client that implements this enables the
// fallback ticker; one that does not (e.g. test fakes) simply runs without it.
type latestLedgerObserver interface {
	GetLatestLedgerSeq(ctx context.Context) (sequence uint32, closeUnix int64, err error)
}

type transactionPollRequest struct {
	ID   int64
	Hash string
}

type transactionPollAttemptResult struct {
	ID       int64
	Hash     string
	Response protocol.GetTransactionResponse
	Err      error
}

type sdkTransactionPollClient struct {
	rpc *rpcclient.Client
}

func newSDKTransactionPollClient(rpc *rpcclient.Client) sdkTransactionPollClient {
	return sdkTransactionPollClient{rpc: rpc}
}

// GetLatestLedgerSeq returns the current network ledger sequence and close
// time. Note the underlying getLatestLedger response also carries the full
// ledger metadata XDR, which we deliberately ignore -- we only read the
// sequence and close time. The fallback ticker keeps this call rare.
func (c sdkTransactionPollClient) GetLatestLedgerSeq(ctx context.Context) (uint32, int64, error) {
	if c.rpc == nil {
		return 0, 0, fmt.Errorf("missing RPC client")
	}
	resp, err := c.rpc.GetLatestLedger(ctx)
	if err != nil {
		return 0, 0, err
	}
	return resp.Sequence, resp.LedgerCloseTime, nil
}

func (c sdkTransactionPollClient) GetTransactions(ctx context.Context, requests []transactionPollRequest) ([]transactionPollAttemptResult, error) {
	if c.rpc == nil {
		return nil, fmt.Errorf("missing RPC client")
	}

	results := make([]transactionPollAttemptResult, len(requests))
	for i, request := range requests {
		result := transactionPollAttemptResult{ID: request.ID, Hash: request.Hash}
		if err := ctx.Err(); err != nil {
			result.Err = err
			results[i] = result
			continue
		}

		resp, err := c.rpc.GetTransaction(ctx, protocol.GetTransactionRequest{Hash: request.Hash})
		result.Response = resp
		result.Err = err
		results[i] = result
	}
	return results, nil
}
