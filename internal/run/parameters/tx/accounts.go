package tx

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type WorkerAccount struct {
	Keypair *keypair.Full
	Id      atomic.Int64  // unique ID incr per tx
	Balance atomic.Uint64 // track balance
}

func (w *WorkerAccount) SendPaymentTo(ctx context.Context, pool *AccountPool, to *WorkerAccount, amount uint64, networkPassphrase string) error {
	txB64, err := BuildSendPaymentTxB64(w, to, amount, w.Id.Load(), networkPassphrase)
	if err != nil {
		return fmt.Errorf("failed to build XDR for funding payment: %v", err)
	}
	if _, err := pool.rpcClient.SendTransaction(ctx, util.MakeSendTransactionRequest(txB64)); err != nil {
		return fmt.Errorf("failed to submit funding transaction: %v", err)
	}
	w.Id.Add(1) // increment sequence for next tx
	w.Balance.Swap(w.Balance.Load() - amount)
	to.Balance.Add(amount)
	return nil
}

func (w *WorkerAccount) GetAccountBalance(ctx context.Context, rpcClient *rpcclient.Client) (int64, error) {
	accountID, err := xdr.AddressToAccountId(w.Keypair.Address())
	if err != nil {
		return 0, fmt.Errorf("failed to convert address to account ID: %v", err)
	}
	lk, err := accountID.LedgerKey()
	if err != nil {
		return 0, fmt.Errorf("failed to get ledger key: %v", err)
	}
	accountKey, err := xdr.MarshalBase64(lk)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal ledger key: %v", err)
	}

	resp, err := rpcClient.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{
		Keys: []string{accountKey},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get ledger entries: %v", err)
	}
	if len(resp.Entries) != 1 {
		return 0, fmt.Errorf("account %s not found", w.Keypair.Address())
	}

	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(resp.Entries[0].DataXDR, &entry); err != nil {
		return 0, fmt.Errorf("failed to unmarshal ledger entry data: %v", err)
	}
	return int64(entry.Account.Balance), nil // balance in stroops (1 XLM = 10^7 stroops)
}

func (w *WorkerAccount) getAccountSeq(ctx context.Context, rpcClient *rpcclient.Client) (int64, error) {
	if seq := w.Id.Load(); seq > 0 {
		return seq, nil
	}
	accountDetail, err := rpcClient.LoadAccount(ctx, w.Keypair.Address())
	if err != nil {
		return 0, fmt.Errorf("failed to get account detail for %s: %v", w.Keypair.Address(), err)
	}
	seq, err := accountDetail.GetSequenceNumber()
	if err != nil {
		return 0, fmt.Errorf("failed to get sequence number for %s: %v", w.Keypair.Address(), err)
	}
	return seq, nil
}

func (w *WorkerAccount) fundTestnetAccount(friendbotURL string) error {
	resp, err := http.Get(friendbotURL + "?" + url.Values{"addr": []string{w.Keypair.Address()}}.Encode())
	if err != nil {
		return fmt.Errorf("friendbot request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("friendbot returned non-200 status: %d", resp.StatusCode)
	}

	return nil
}

func VerifyOnChainAndStore(ctx context.Context, rpcClient *rpcclient.Client, accounts []*WorkerAccount) (bool, error) {
	for attempt := range 10 {
		var err error
		allFound := true
		for _, acct := range accounts {
			if seq, err := acct.getAccountSeq(ctx, rpcClient); err != nil || seq <= 0 {
				allFound = false
				continue
			} else {
				acct.Id.Store(seq)
			}
		}
		if allFound {
			break
		}
		if attempt == 9 {
			return false, fmt.Errorf("some accounts not visible on-chain after funding: %v", err)
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}

	return true, nil
}
