package tx

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type mergeSubmission struct {
	account *WorkerAccount
	hash    string
}

type AccountPool struct {
	rpcClient    *rpcclient.Client
	passphrase   string
	friendbotURL string
	logger       *log.Entry

	PoolBalance   int64          // track total balance across all accounts in the pool for verification after test
	originAccount *WorkerAccount // account that funds the worker accounts
	accounts      []*WorkerAccount
	idx           int64 // atomically incr index to rotate through accounts

	mu sync.Mutex
}

// Makes a pool of workers and funds each of them with testnet friendbot XLM
func NewAccountPool(
	ctx context.Context,
	logger *log.Entry,
	rpcClient *rpcclient.Client,
	originAccountKp *keypair.Full, // optional param to specify funding account
	numAccounts uint32,
) (*AccountPool, error) {
	pool := &AccountPool{
		rpcClient: rpcClient,
		logger:    logger,
	}
	// Validate and set up network based on its type
	getNetworkResponse, err := pool.rpcClient.GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network info: %v", err)
	}
	network, err := util.IsPubnetOrTestnet(getNetworkResponse.Passphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to determine network type: %v", err)
	}
	if network == "testnet" {
		pool.friendbotURL = getNetworkResponse.FriendbotURL
	} else if originAccountKp == nil {
		return nil, fmt.Errorf("origin account keypair must be provided for pubnet")
	}
	pool.passphrase = getNetworkResponse.Passphrase

	// Set up origin account struct
	originAccount, err := pool.NewOriginAccount(ctx, originAccountKp, network)
	if err != nil {
		return nil, fmt.Errorf("failed to create and fund origin account: %v", err)
	}
	pool.originAccount = originAccount

	originAccountBalance, err := originAccount.GetOnChainBalance(ctx, rpcClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance for origin account: %v", err)
	}
	pool.PoolBalance = originAccountBalance

	budgetPerWorker := int64(util.MinimumWorkerBalance)
	// Reserve 100 XLM in the origin account so it can remain the sole fee payer throughout the run.
	if budgetPerWorker*int64(numAccounts) > originAccountBalance-100 {
		return nil, fmt.Errorf("origin account does not have enough balance to minimally fund workers and pay fees")
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
	totalAccounts := len(p.accounts)
	deadline := time.Now().Add(util.WorkerMergeRetryWindow)

	// Try merging until the deadline or all accounts are merged
	for attempt := 0; len(p.accounts) > 0; attempt++ {
		submissions := make([]mergeSubmission, 0, len(p.accounts))

		for _, acct := range p.accounts {
			hash, err := acct.MergeInto(ctx, p, p.originAccount)
			if err != nil {
				logger.Errorf("failed to submit merge for worker account %s on attempt %d: %v", acct.Keypair.Address(), attempt, err)
				continue
			}
			submissions = append(submissions, mergeSubmission{account: acct, hash: hash})
		}

		awaitMergeConfirmations(ctx, p, p.rpcClient, logger, submissions, attempt)
		remaining := time.Until(deadline)
		if len(p.accounts) == 0 || remaining <= 0 {
			break
		}

		logger.Warnf("Retrying merge for %d worker accounts after attempt %d", len(p.accounts), attempt)
		pauseFloat64 := math.Pow(float64(util.WorkerMergeRetryPause), float64(attempt))
		pause := min(time.Duration(int64(pauseFloat64)), remaining)
		if pause <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			logger.Warnf("stopping merge retries early: %v", ctx.Err())
		case <-time.After(pause): // exponential backoff capped at remaining time until deadline
		}
	}

	// If we failed to merge any accounts, write their seeds to a file in ./output (safety: it's in .gitignore)
	if len(p.accounts) > 0 {
		if err := p.writeUnmergedWorkersJSON(util.UnmergedWorkersPath); err != nil {
			return fmt.Errorf("failed to merge %d worker accounts after retries and failed to write %s: %w",
				len(p.accounts), util.UnmergedWorkersPath, err)
		}
		logger.Errorf("Failed to merge %d worker accounts after retries; wrote seeds to %s",
			len(p.accounts), util.UnmergedWorkersPath)
	}

	if err := p.verifyOriginBalance(ctx, rpcClient); err != nil {
		logger.Errorf("Origin account balance verification failed after merging workers: %v", err)
	}

	logger.Infof("Successfully merged %d worker accounts back into origin account",
		totalAccounts-len(p.accounts))
	if len(p.accounts) > 0 {
		return fmt.Errorf("failed to merge %d worker accounts after retrying for at least %s",
			len(p.accounts), util.WorkerMergeRetryWindow)
	}
	return nil
}

// Creates and funds all workers in the pool with the specified amount from the origin account
func (p *AccountPool) FundWorkers(
	ctx context.Context,
	budgetPerWorker int64,
	numWorkers uint32,
) ([]*WorkerAccount, error) {
	if budgetPerWorker*int64(numWorkers) > p.originAccount.Balance.Load() {
		return nil, fmt.Errorf("origin account does not have enough balance to fund workers")
	}
	sa := p.originAccount
	accounts := make([]*WorkerAccount, numWorkers)
	for i := range accounts {
		workerKp, err := keypair.Random()
		if err != nil {
			return nil, fmt.Errorf("failed to generate random keypair: %v", err)
		}
		accounts[i] = &WorkerAccount{Keypair: workerKp}
	}

	// Create worker accounts in batches
	for start := 0; start < len(accounts); start += util.MaxCreateAccountsPerTx {
		end := start + util.MaxCreateAccountsPerTx
		end = min(end, len(accounts))
		batch := accounts[start:end]

		hash, err := sa.CreateAccountsFor(
			ctx,
			p,
			batch,
			budgetPerWorker,
			p.passphrase,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to fund worker accounts %d-%d: %v", start, end-1, err)
		}
		if err := awaitTxConfirmation(ctx, p.rpcClient, hash); err != nil {
			return nil, fmt.Errorf("failed to confirm funding tx for accounts %d-%d: %v", start, end-1, err)
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
func (p *AccountPool) NewOriginAccount(
	ctx context.Context,
	originAccountKp *keypair.Full,
	network string,
) (*WorkerAccount, error) {
	acct := &WorkerAccount{}
	if originAccountKp != nil {
		acct = &WorkerAccount{Keypair: originAccountKp}
	} else if network == "testnet" {
		p.logger.Infof("No origin account provided in config, creating a new origin account for testnet...")
		kp, err := keypair.Random()
		if err != nil {
			return nil, fmt.Errorf("failed to generate random keypair: %v", err)
		}
		acct = &WorkerAccount{Keypair: kp}
		if err := acct.fundTestnetAccount(p.friendbotURL); err != nil {
			return nil, fmt.Errorf("failed to fund origin account %s: %v", kp.Address(), err)
		}
		p.logger.Infof("Created + funded origin account on testnet with seed %s", kp.Seed())
	} else {
		return nil, fmt.Errorf("origin account keypair must be provided for %s", network)
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

// WrapWithOriginFeeBumpB64 wraps a signed inner transaction so the origin account pays the fee.
func (p *AccountPool) WrapWithOriginFeeBumpB64(innerTx *txnbuild.Transaction) (string, error) {
	feeBumpTx, err := txnbuild.NewFeeBumpTransaction(txnbuild.FeeBumpTransactionParams{
		Inner:      innerTx,
		FeeAccount: p.originAccount.Keypair.Address(),
		BaseFee:    innerTx.BaseFee(),
	})
	if err != nil {
		return "", fmt.Errorf("couldn't create fee bump transaction: %w", err)
	}

	feeBumpTx, err = feeBumpTx.Sign(p.passphrase, p.originAccount.Keypair)
	if err != nil {
		return "", fmt.Errorf("couldn't sign fee bump transaction: %w", err)
	}

	return feeBumpTx.Base64()
}

func awaitMergeConfirmations(
	ctx context.Context,
	pool *AccountPool,
	rpcClient *rpcclient.Client,
	logger *log.Entry,
	submissions []mergeSubmission,
	attempt int,
) {
	if len(submissions) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, submission := range submissions {
		wg.Go(func() {
			if err := awaitTxConfirmation(ctx, rpcClient, submission.hash); err != nil {
				logger.Errorf("failed to confirm merge tx %s for worker account %s on attempt %d: %v",
					submission.hash, submission.account.Keypair.Address(), attempt, err)
				return
			}
			pool.removeWorkerAccount(submission.account.Keypair.Address())
		})
	}
	wg.Wait()
}

func (p *AccountPool) removeWorkerAccount(address string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, acct := range p.accounts {
		if acct.Keypair.Address() != address {
			continue
		}
		p.accounts = append(p.accounts[:i], p.accounts[i+1:]...)
		return
	}
}

func (p *AccountPool) writeUnmergedWorkersJSON(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create output directories for path %s: %v", path, err)
	}

	seeds := make([]string, 0, len(p.accounts))
	for _, acct := range p.accounts {
		seeds = append(seeds, acct.Keypair.Seed())
	}
	slices.Sort(seeds)

	data, err := json.MarshalIndent(seeds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal unmerged worker seeds: %v", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("failed to write unmerged workers to %s: %v", path, err)
	}

	return nil
}

func (p *AccountPool) verifyOriginBalance(ctx context.Context, rpcClient *rpcclient.Client) error {
	balance, err := p.originAccount.GetOnChainBalance(ctx, rpcClient)
	if err != nil {
		return err
	}
	if balance > p.PoolBalance {
		return fmt.Errorf("origin balance increased unexpectedly after consolidation: started %d, ended %d",
			p.PoolBalance, balance)
	}
	return fmt.Errorf("Origin account balance after consolidation: %d (approximately %d XLM spent on fees)",
		balance, p.PoolBalance-balance)
}
