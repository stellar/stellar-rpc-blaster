package state

import (
	"context"
	"strings"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
)

// AccountExists returns true if the given address has a ledger entry on-chain.
// LoadAccount returns a plain-text error when the account is absent; we detect
// that specific message rather than a typed sentinel.
func AccountExists(ctx context.Context, rpc *rpcclient.Client, address string) (bool, error) {
	_, err := rpc.LoadAccount(ctx, address)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "failed to find ledger entry") {
		return false, nil
	}
	return false, err
}
