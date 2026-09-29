package setup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const (
	stroopsPerXLM     = 10_000_000
	setupFeeBudgetXLM = 100.0 // flat XLM buffer reserved for all setup transaction fees
)

// parseFeePayer derives a Full keypair from a raw secret seed string.
func parseFeePayer(seed string) (*keypair.Full, error) {
	kp, err := keypair.ParseFull(seed)
	if err != nil {
		return nil, fmt.Errorf("invalid fee-payer seed: %w", err)
	}
	return kp, nil
}

type feePayerStep struct{}

func (feePayerStep) Name() string { return "initialize fee payer" }

// Run parses the fee-payer seed, creates an RPC client, verifies the network
// passphrase, funds the account via friendbot when absent on testnet, and
// checks that the balance is sufficient to cover all planned setup spending.
//
// When cfg.FeePayerSeed is empty a fresh random keypair is generated and
// funded via friendbot. This is only supported on networks that expose a
// friendbot URL (testnet / futurenet). The generated seed is printed to the
// log so the caller can record it for subsequent bench runs.
func (feePayerStep) Run(ctx context.Context, logger *log.Entry, cfg config.Config, st *state.State) error {
	rpc := rpcclient.NewClient(cfg.RPCURL, nil)

	// Fetch network metadata first so we can use FriendbotURL for auto-funding.
	netInfo, err := rpc.GetNetwork(ctx)
	if err != nil {
		return fmt.Errorf("fee payer: get network: %w", err)
	}
	if netInfo.Passphrase != cfg.NetworkPassphrase {
		return fmt.Errorf(
			"fee payer: network passphrase mismatch: config has %q but RPC reports %q",
			cfg.NetworkPassphrase, netInfo.Passphrase,
		)
	}

	var kp *keypair.Full
	if cfg.FeePayerSeed == "" {
		// No seed provided  -- generate a fresh keypair and fund it via friendbot.
		if netInfo.FriendbotURL == "" {
			return fmt.Errorf(
				"fee payer: no seed provided and this network has no friendbot -- " +
					"set --fee-payer-seed or the TX_LOAD_TEST_FEE_PAYER_SEED environment variable",
			)
		}
		kp, err = keypair.Random()
		if err != nil {
			return fmt.Errorf("fee payer: generate keypair: %w", err)
		}
		logger.Warnf("no seed provided  -- generating a temporary keypair (only suitable for development)")
		logger.Warnf("address: %s", kp.Address())
		logger.Warnf("seed (set TX_LOAD_TEST_FEE_PAYER_SEED to reuse across runs): %s", kp.Seed())
		logger.Infof("funding via friendbot at %s", netInfo.FriendbotURL)
		if err = fundViaFriendbot(ctx, netInfo.FriendbotURL, kp.Address()); err != nil {
			return fmt.Errorf("fee payer: friendbot: %w", err)
		}
		logger.Infof("funded successfully")
	} else {
		kp, err = parseFeePayer(cfg.FeePayerSeed)
		if err != nil {
			return err
		}
		logger.Infof("account: %s", kp.Address())

		// Fund via friendbot if the account does not yet exist.
		exists, existsErr := state.AccountExists(ctx, rpc, kp.Address())
		if existsErr != nil {
			return fmt.Errorf("fee payer: check account: %w", existsErr)
		}
		if !exists {
			if netInfo.FriendbotURL == "" {
				return fmt.Errorf(
					"fee payer: account %s does not exist and no friendbot is available",
					kp.Address(),
				)
			}
			logger.Infof("account not found, funding via friendbot at %s", netInfo.FriendbotURL)
			if err = fundViaFriendbot(ctx, netInfo.FriendbotURL, kp.Address()); err != nil {
				return fmt.Errorf("fee payer: friendbot: %w", err)
			}
			logger.Infof("funded successfully")
		}
	}

	// Verify the account holds enough XLM to cover all setup spending.
	// If the balance is insufficient and a friendbot is available, repeatedly
	// fund a temporary account and merge it into the fee payer until the
	// required balance is reached.
	balance, err := nativeBalanceXLM(ctx, rpc, kp.Address())
	if err != nil {
		return fmt.Errorf("fee payer: read balance: %w", err)
	}
	minRequired := float64(cfg.NumberOfAccounts)*cfg.BaseReserveXLM + setupFeeBudgetXLM
	for balance < minRequired {
		if netInfo.FriendbotURL == "" {
			return fmt.Errorf(
				"fee payer: insufficient balance: have %.2f XLM, need at least %.2f XLM "+
					"(no friendbot available to top up)",
				balance, minRequired,
			)
		}
		logger.Infof("balance %.2f XLM < required %.2f XLM  -- topping up via friendbot", balance, minRequired)
		if err := topUpViaFriendbot(ctx, logger, rpc, cfg.NetworkPassphrase, netInfo.FriendbotURL, kp); err != nil {
			return fmt.Errorf("fee payer: top-up: %w", err)
		}
		balance, err = nativeBalanceXLM(ctx, rpc, kp.Address())
		if err != nil {
			return fmt.Errorf("fee payer: read balance after top-up: %w", err)
		}
	}
	logger.Infof("balance %.2f XLM (min required %.2f XLM)", balance, minRequired)

	st.RPCClient = rpc
	st.FeePayerKP = kp
	st.NetworkPassphrase = cfg.NetworkPassphrase
	return nil
}

// fundViaFriendbot sends a GET request to the friendbot URL with the target
// address and returns an error for any non-2xx response.
func fundViaFriendbot(ctx context.Context, friendbotURL, address string) error {
	url := friendbotURL + "?addr=" + address
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// topUpViaFriendbot creates a temporary account via friendbot and merges it
// into the fee payer, transferring its entire balance (~10 000 XLM on testnet).
// This is repeated by the caller until the fee payer reaches the required
// balance.
func topUpViaFriendbot(
	ctx context.Context,
	logger *log.Entry,
	rpc *rpcclient.Client,
	networkPassphrase string,
	friendbotURL string,
	feePayerKP *keypair.Full,
) error {
	tmpKP, err := keypair.Random()
	if err != nil {
		return fmt.Errorf("generate temp keypair: %w", err)
	}
	logger.Debugf("funding temp account %s via friendbot", tmpKP.Address())
	if err := fundViaFriendbot(ctx, friendbotURL, tmpKP.Address()); err != nil {
		return fmt.Errorf("fund temp account: %w", err)
	}
	logger.Debugf("merging temp account %s into fee payer", tmpKP.Address())
	if err := state.SubmitAndWait(ctx, logger, rpc, networkPassphrase, tmpKP, state.InclusionFee, []txnbuild.Operation{
		&txnbuild.AccountMerge{Destination: feePayerKP.Address()},
	}); err != nil {
		return fmt.Errorf("merge temp account: %w", err)
	}
	return nil
}

// nativeBalanceXLM returns the native XLM balance of the account in XLM units.
// It fetches the raw account ledger entry and reads the balance field directly
// from the XDR, avoiding the need for a separate account-info endpoint.
func nativeBalanceXLM(ctx context.Context, rpc *rpcclient.Client, address string) (float64, error) {
	accountID, err := xdr.AddressToAccountId(address)
	if err != nil {
		return 0, err
	}
	lk, err := accountID.LedgerKey()
	if err != nil {
		return 0, err
	}
	accountKey, err := xdr.MarshalBase64(lk)
	if err != nil {
		return 0, err
	}
	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{accountKey}})
	if err != nil {
		return 0, err
	}
	if len(resp.Entries) != 1 {
		return 0, fmt.Errorf("account %s not found on ledger", address)
	}
	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(resp.Entries[0].DataXDR, &entry); err != nil {
		return 0, err
	}
	if entry.Account == nil {
		return 0, fmt.Errorf("ledger entry for %s is not an account", address)
	}
	return float64(entry.Account.Balance) / stroopsPerXLM, nil
}
