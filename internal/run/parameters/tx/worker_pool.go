package tx

import (
	"context"
	"fmt"
	"sync"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type AccountPool struct {
	rpcClient    *rpcclient.Client
	passphrase   string
	friendbotURL string

	PoolBalance   int64          // track total balance across all accounts in the pool for verification at the end of the test
	originAccount *WorkerAccount // account that funds the worker accounts
	accounts      []*WorkerAccount
	idx           int64 // atomically incr index to rotate through accounts

	mu sync.Mutex
}

// Makes a pool of workers and funds each of them with testnet friendbot XLM
func NewTestnetAccountPool(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	originAccountKp *keypair.Full, // optional param to specify funding account
	numAccounts uint32,
) (*AccountPool, error) {
	pool := &AccountPool{
		rpcClient: rpcClient,
	}
	getNetworkResponse, err := pool.rpcClient.GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network info: %v", err)
	}
	pool.friendbotURL = getNetworkResponse.FriendbotURL
	pool.passphrase = getNetworkResponse.Passphrase

	originAccount, err := pool.NewTestnetOriginAccount(ctx, originAccountKp)
	if err != nil {
		return nil, fmt.Errorf("failed to create and fund origin account: %v", err)
	}
	pool.originAccount = originAccount

	originAccountBalance, err := originAccount.GetOnChainBalance(ctx, rpcClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance for origin account: %v", err)
	}
	pool.PoolBalance = originAccountBalance

	// Reserve 100 XLM in the origin account for base reserve + tx fees
	spendable := originAccountBalance - 100
	budgetPerWorker := spendable / int64(numAccounts)
	if budgetPerWorker <= util.MinimumWorkerBalance {
		return nil, fmt.Errorf("origin account does not have enough balance to fund workers")
	}

	// Fund worker accounts to ensure they have enough balance for the test
	accounts, err := pool.FundWorkers(ctx, budgetPerWorker, numAccounts)
	if err != nil {
		return nil, fmt.Errorf("failed to fund worker accounts: %v", err)
	}
	pool.accounts = accounts
	return pool, nil
}

func (p *AccountPool) Close(ctx context.Context, rpcClient *rpcclient.Client, logger *log.Entry) error {
	// Submit all merge txs first, then confirm all at once.
	// Each worker is a separate source account so there are no sequence dependencies.
	hashes := make([]string, 0, len(p.accounts))
	for _, acct := range p.accounts {
		hash, err := acct.MergeInto(ctx, p, p.originAccount, p.passphrase)
		if err != nil {
			logger.Errorf("failed to merge worker account %s: %v", acct.Keypair.Address(), err)
			continue
		}
		hashes = append(hashes, hash)
	}

	// Await all merge confirmations
	for _, hash := range hashes {
		if err := awaitTxConfirmation(ctx, p.rpcClient, hash); err != nil {
			logger.Errorf("failed to confirm merge tx %s: %v", hash, err)
		}
	}
	balance, err := p.originAccount.GetOnChainBalance(ctx, rpcClient)
	if err != nil {
		logger.Errorf("balance verification failed after merging worker accounts back into origin account: %v", err)
	} else if balance < p.PoolBalance-util.FeeTolerance {
		logger.Errorf("balance verification failed after consolidating accounts: expected %d, got %d",
			p.PoolBalance, balance)
	}

	logger.Infof("Successfully merged %d worker accounts back into origin account", len(hashes))
	return nil
}

// Creates and funds all workers in the pool with the specified amount from the origin account
func (p *AccountPool) FundWorkers(ctx context.Context, budgetPerWorker int64, numWorkers uint32) ([]*WorkerAccount, error) {
	if budgetPerWorker*int64(numWorkers) > p.originAccount.Balance.Load() {
		return nil, fmt.Errorf("origin account does not have enough balance to fund workers")
	}
	sa := p.originAccount
	accounts := make([]*WorkerAccount, numWorkers)
	// Submit and confirm each CreateAccount tx one at a time
	for i := range accounts {
		workerKp, err := keypair.Random()
		if err != nil {
			return nil, fmt.Errorf("failed to generate random keypair: %v", err)
		}
		accounts[i] = &WorkerAccount{Keypair: workerKp}

		hash, err := sa.CreateAccountFor(
			ctx,
			p,
			accounts[i],
			budgetPerWorker,
			p.passphrase,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to fund worker account %s: %v", workerKp.Address(), err)
		}
		if err := awaitTxConfirmation(ctx, p.rpcClient, hash); err != nil {
			return nil, fmt.Errorf("failed to confirm funding tx for %s: %v", workerKp.Address(), err)
		}
	}
	// Fetch and store sequence numbers for all accounts
	if err := VerifyOnChainSeqAndStore(ctx, p.rpcClient, accounts); err != nil {
		return nil, fmt.Errorf("failed to verify accounts on-chain: %v", err)
	}

	// Verify all accounts have the correct balance on chain and store the balance in each account struct
	// Serves as a final check that all funding transactions were successful and the accounts are ready to use
	if err := VerifyOnChainBalanceAndStore(ctx, p.rpcClient, accounts, budgetPerWorker); err != nil {
		return nil, fmt.Errorf("failed to verify balance for worker accounts: %v", err)
	}

	return accounts, nil
}

// Creates a new origin account using friendbot
func (p *AccountPool) NewTestnetOriginAccount(ctx context.Context, originAccountKp *keypair.Full) (*WorkerAccount, error) {
	acct := &WorkerAccount{}
	if originAccountKp != nil {
		acct = &WorkerAccount{Keypair: originAccountKp}
	} else {
		kp, err := keypair.Random()
		if err != nil {
			return nil, fmt.Errorf("failed to generate random keypair: %v", err)
		}
		acct = &WorkerAccount{Keypair: kp}
		if err := acct.fundTestnetAccount(p.friendbotURL); err != nil {
			return nil, fmt.Errorf("failed to fund origin account %s: %v", kp.Address(), err)
		}
	}

	if err := VerifyOnChainSeqAndStore(ctx, p.rpcClient, []*WorkerAccount{acct}); err != nil {
		return nil, fmt.Errorf("failed to verify origin account on-chain: %v", err)
	}

	balance, err := acct.GetOnChainBalance(ctx, p.rpcClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance for origin account %s: %v", acct.Keypair.Address(), err)
	}
	acct.Balance.Store(balance)

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
