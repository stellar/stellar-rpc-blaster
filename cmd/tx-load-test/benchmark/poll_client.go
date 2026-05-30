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
