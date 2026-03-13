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
	proto "github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stellar/stellar-rpc-blaster/internal/util"
)

type WorkerAccount struct {
	Keypair *keypair.Full
	Id      atomic.Int64 // unique ID incr per tx
	Balance atomic.Int64 // track balance
}

func (w *WorkerAccount) SendPaymentTo(
	ctx context.Context,
	pool *AccountPool,
	to *WorkerAccount,
	amount int64,
	networkPassphrase string,
) (string, error) {
	seq, err := w.fetchOnChainSeq(ctx, pool.rpcClient)
	if err != nil {
		return "", fmt.Errorf("failed to get on-chain sequence for %s: %v", w.Keypair.Address(), err)
	}
	tx, err := BuildSendPaymentTx(w, to, amount, seq, networkPassphrase)
	if err != nil {
		return "", fmt.Errorf("failed to build XDR for funding payment: %v", err)
	}
	txB64, err := tx.Base64()
	if err != nil {
		return "", fmt.Errorf("failed to encode transaction to base64: %v", err)
	}
	resp, err := pool.rpcClient.SendTransaction(ctx, util.MakeSendTransactionRequest(txB64))
	if err != nil {
		return "", fmt.Errorf("failed to submit funding transaction: %v", err)
	}
	w.Id.Store(seq + 1) // store confirmed next sequence
	w.Balance.Swap(w.Balance.Load() - amount)
	to.Balance.Add(amount)
	return resp.Hash, nil
}

// MergeInto closes this account and transfers all remaining funds to the destination account.
func (w *WorkerAccount) MergeInto(ctx context.Context, pool *AccountPool, to *WorkerAccount) (string, error) {
	seq, err := w.fetchOnChainSeq(ctx, pool.rpcClient)
	if err != nil {
		return "", fmt.Errorf("failed to get on-chain sequence for %s: %v", w.Keypair.Address(), err)
	}

	innerTx, err := BuildAccountMergeTx(w, to, seq, pool.passphrase)
	if err != nil {
		return "", fmt.Errorf("failed to build account merge tx: %v", err)
	}
	txB64, err := pool.WrapWithOriginFeeBumpB64(innerTx)
	if err != nil {
		return "", fmt.Errorf("failed to wrap account merge tx with origin fee bump: %v", err)
	}
	resp, err := pool.rpcClient.SendTransaction(ctx, util.MakeSendTransactionRequest(txB64))
	if err != nil {
		return "", fmt.Errorf("failed to submit account merge transaction: %v", err)
	}
	return resp.Hash, nil
}

func (w *WorkerAccount) CreateAccountsFor(
	ctx context.Context,
	pool *AccountPool,
	to []*WorkerAccount,
	amount int64,
	networkPassphrase string,
) (string, error) {
	seq, err := w.fetchOnChainSeq(ctx, pool.rpcClient)
	if err != nil {
		return "", fmt.Errorf("failed to get on-chain sequence for %s: %v", w.Keypair.Address(), err)
	}

	tx, err := BuildCreateAccountsTx(w, to, amount, seq, networkPassphrase)
	if err != nil {
		return "", fmt.Errorf("failed to build create account tx: %v", err)
	}
	txB64, err := tx.Base64()
	if err != nil {
		return "", fmt.Errorf("failed to encode transaction to base64: %v", err)
	}

	resp, err := pool.rpcClient.SendTransaction(ctx, util.MakeSendTransactionRequest(txB64))
	if err != nil {
		return "", fmt.Errorf("failed to submit create account transaction: %v", err)
	}
	if resp.Status == proto.TXStatusError {
		return "", fmt.Errorf("create account transaction rejected: %s", resp.ErrorResultXDR)
	}

	w.Id.Store(seq + 1)
	w.Balance.Swap(w.Balance.Load() - amount*int64(len(to)))
	for _, acct := range to {
		acct.Balance.Store(amount)
	}
	return resp.Hash, nil
}

// awaitTxConfirmation polls getTransaction for each hash until it is confirmed on-chain (SUCCESS or FAILED).
// sendTransaction is async, so this is needed to ensure accounts exist before using them.
func awaitTxConfirmation(ctx context.Context, rpcClient *rpcclient.Client, txHash string) error {
	for attempt := range 30 {
		resp, err := rpcClient.GetTransaction(ctx, protocol.GetTransactionRequest{Hash: txHash})
		if err != nil {
			return fmt.Errorf("getTransaction failed for %s: %v", txHash, err)
		}
		switch resp.Status {
		case protocol.TransactionStatusSuccess:
			return nil
		case protocol.TransactionStatusFailed:
			return fmt.Errorf("transaction %s failed on-chain", txHash)
		}
		if attempt == 29 {
			return fmt.Errorf("transaction %s not confirmed after 30 attempts", txHash)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil
}

func (w *WorkerAccount) GetOnChainBalance(ctx context.Context, rpcClient *rpcclient.Client) (int64, error) {
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
	xlmBalance := entry.Account.Balance / util.StroopsPerXLM

	return int64(xlmBalance), nil // balance in whole XLM units
}

// fetchOnChainSeq always fetches the current sequence from the network.
// Used by setup/teardown operations that need the true on-chain state.
func (w *WorkerAccount) fetchOnChainSeq(ctx context.Context, rpcClient *rpcclient.Client) (int64, error) {
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

// Verify an account's sequence number is correctly stored on chain and store it in the account struct
// Retries for up to ~30 seconds total since there may be some delay between funding tx confirmation and account visibility on-chain
func VerifyOnChainSeqAndStore(ctx context.Context, rpcClient *rpcclient.Client, accounts []*WorkerAccount) error {
	var lastErr error
	for attempt := range 10 {
		allFound := true
		for _, acct := range accounts {
			seq, err := acct.fetchOnChainSeq(ctx, rpcClient)
			if err != nil || seq <= 0 {
				allFound = false
				lastErr = err
				continue
			}
			acct.Id.Store(seq)
		}
		if allFound {
			return nil
		}
		if attempt == 9 {
			return fmt.Errorf("some accounts not visible on-chain after funding: %v", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}

	return nil
}

// Verify all account's balances are correct on chain and stores the balance in each account struct
// Retries for up to ~30 seconds total to account for any possible delays
func VerifyOnChainBalanceAndStore(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	accounts []*WorkerAccount,
	expectedBalance int64,
) error {
	var lastErr error
	for attempt := range 10 {
		allFound := true
		for _, acct := range accounts {
			balance, err := acct.GetOnChainBalance(ctx, rpcClient)
			if err != nil {
				allFound = false
				lastErr = err
				continue
			}
			if balance != expectedBalance {
				allFound = false
				lastErr = fmt.Errorf("unexpected balance for worker account %s: got %d, expected %d",
					acct.Keypair.Address(), balance, expectedBalance)
				continue
			}
			acct.Balance.Store(balance)
		}
		if allFound {
			return nil
		}
		if attempt == 9 {
			return fmt.Errorf("some accounts not visible on-chain after funding: %v", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
	return nil
}
