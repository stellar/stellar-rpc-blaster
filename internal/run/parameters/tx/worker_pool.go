package tx

import (
	"context"
	"fmt"
	"sync"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
)

type AccountPool struct {
	rpcClient     *rpcclient.Client
	passphrase    string
	originAccount *WorkerAccount // account that funds the worker accounts
	accounts      []*WorkerAccount
	idx           int64 // atomically incr index to rotate through accounts
	mu            sync.Mutex
}

// Makes a pool of workers and funds each of them with testnet friendbot XLM
func NewTestnetAccountPool(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	numAccounts uint32,
) (*AccountPool, error) {
	pool := &AccountPool{
		rpcClient: rpcClient,
	}
	originAccount, err := pool.NewTestnetOriginAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create and fund origin account: %v", err)
	}
	pool.originAccount = originAccount

	// Fund worker accounts with 10k XLM each to ensure they have enough balance for the test
	accounts, err := pool.FundWorkers(ctx, 10000, numAccounts)
	if err != nil {
		return nil, fmt.Errorf("failed to fund worker accounts: %v", err)
	}
	pool.accounts = accounts
	return pool, nil
}

func (p *AccountPool) Close() {
	// direct all funds from worker accounts back to origin account
}

// Creates and funds all workers in the pool with the specified amount from the origin account
func (p *AccountPool) FundWorkers(ctx context.Context, amountPerWorker uint64, numWorkers uint32) ([]*WorkerAccount, error) {
	if amountPerWorker*uint64(numWorkers) > p.originAccount.Balance.Load() {
		return nil, fmt.Errorf("origin account does not have enough balance to fund workers")
	}
	sa := p.originAccount
	accounts := make([]*WorkerAccount, numWorkers)
	// For each worker account, send a payment from the origin account to fund it
	for i := range accounts {
		workerKp, err := keypair.Random()
		if err != nil {
			return nil, fmt.Errorf("failed to generate random keypair: %v", err)
		}
		accounts[i] = &WorkerAccount{Keypair: workerKp}

		if err := sa.SendPaymentTo(ctx, p, accounts[i], amountPerWorker, p.passphrase); err != nil {
			return nil, fmt.Errorf("failed to fund worker account %s: %v", workerKp.Address(), err)
		}
	}
	// Wait for ledger close so all accounts visible on-chain, then fetch all sequence numbers
	if ok, err := VerifyOnChainAndStore(ctx, p.rpcClient, accounts); err != nil || !ok {
		return nil, fmt.Errorf("failed to verify accounts on-chain: %v", err)
	}

	return accounts, nil
}

// Creates a new origin account using friendbot
func (p *AccountPool) NewTestnetOriginAccount(ctx context.Context) (*WorkerAccount, error) {
	friendbotUrlReq, err := p.rpcClient.GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get friendbot URL: %v", err)
	} else if friendbotUrlReq.FriendbotURL == "" {
		return nil, fmt.Errorf("network does not have friendbot URL (check RPC config?)")
	}

	kp, err := keypair.Random()
	if err != nil {
		return nil, fmt.Errorf("failed to generate random keypair: %v", err)
	}
	acct := &WorkerAccount{Keypair: kp}
	if err := acct.fundTestnetAccount(friendbotUrlReq.FriendbotURL); err != nil {
		return nil, fmt.Errorf("failed to fund origin account %s: %v", kp.Address(), err)
	}

	if ok, err := VerifyOnChainAndStore(ctx, p.rpcClient, []*WorkerAccount{acct}); err != nil || !ok {
		return nil, fmt.Errorf("failed to verify origin account on-chain: %v", err)
	}

	return acct, nil
}

// Get the next account and its current sequence number, and increment the sequence for the next call
func (p *AccountPool) Next() (*WorkerAccount, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.idx
	acct := p.accounts[i%int64(len(p.accounts))]
	p.idx++
	return acct, acct.Id.Add(1) - 1
}
