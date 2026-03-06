package tx

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
)

type WorkerAccount struct {
	Keypair *keypair.Full
	Id      atomic.Int64 // unique ID incr per tx
}

type AccountPool struct {
	accounts []*WorkerAccount
	idx      int64 // atomically incr index to rotate through accounts
	mu       sync.Mutex
}

// Makes a pool of workers and funds each of them with testnet friendbot XLM
func NewTestnetAccountPool(
	ctx context.Context,
	numAccounts uint32,
	rpcClient *rpcclient.Client,
) (*AccountPool, error) {
	friendbotUrlReq, err := rpcClient.GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get friendbot URL: %v", err)
	} else if friendbotUrlReq.FriendbotURL == "" {
		return nil, fmt.Errorf("network does not have friendbot URL (check RPC config?)")
	}

	// Generate keypairs and fund all accounts
	accounts := make([]*WorkerAccount, numAccounts)
	for i := range accounts {
		kp, err := keypair.Random()
		if err != nil {
			return nil, fmt.Errorf("failed to generate random keypair: %v", err)
		}
		accounts[i] = &WorkerAccount{Keypair: kp}

		if err := fundTestnetAccount(kp.Address(), friendbotUrlReq.FriendbotURL); err != nil {
			return nil, fmt.Errorf("failed to fund account %s: %v", kp.Address(), err)
		}
	}

	// Wait for ledger close so all accounts visible on-chain, then fetch all sequence numbers
	for attempt := range 10 {
		allFound := true
		for _, acct := range accounts {
			if acct.Id.Load() != 0 {
				continue // already resolved
			}
			seq, err := getAccountSeq(ctx, acct, rpcClient)
			if err != nil {
				allFound = false
				continue
			}
			acct.Id.Store(seq)
		}
		if allFound {
			break
		}
		if attempt == 9 {
			return nil, fmt.Errorf("some accounts not visible on-chain after funding")
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}

	return &AccountPool{accounts: accounts}, nil
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

func fundTestnetAccount(address string, friendbotURL string) error {
	resp, err := http.Get(friendbotURL + "?" + url.Values{"addr": []string{address}}.Encode())
	if err != nil {
		return fmt.Errorf("friendbot request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("friendbot returned non-200 status: %d", resp.StatusCode)
	}

	return nil
}

func getAccountSeq(ctx context.Context, account *WorkerAccount, rpcClient *rpcclient.Client) (int64, error) {
	accountDetail, err := rpcClient.LoadAccount(ctx, account.Keypair.Address())
	if err != nil {
		return 0, fmt.Errorf("failed to get account detail for %s: %v", account.Keypair.Address(), err)
	}
	seq, err := accountDetail.GetSequenceNumber()
	if err != nil {
		return 0, fmt.Errorf("failed to get sequence number for %s: %v", account.Keypair.Address(), err)
	}
	return seq, nil
}
