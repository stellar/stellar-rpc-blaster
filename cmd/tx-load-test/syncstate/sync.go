// Package syncstate reconciles the local state file with on-chain account
// existence.
package syncstate

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// SyncState checks which participant accounts still exist on-chain
// and rewrites the state file with only those that do. This reconciles the
// local state file with the actual network state.
func SyncState(ctx context.Context, logger *log.Entry, st *state.State, stateFile string) error {
	logger = logger.WithField("phase", "sync")

	var surviving []*keypair.Full
	var removed int
	for _, kp := range st.AccountKPs {
		if kp == nil {
			continue
		}
		exists, err := state.AccountExists(ctx, st.RPCClient, kp.Address())
		if err != nil {
			return fmt.Errorf("check account %s: %w", kp.Address(), err)
		}
		if exists {
			surviving = append(surviving, kp)
		} else {
			removed++
		}
	}

	if removed == 0 {
		logger.Info("all accounts exist on-chain  -- state file is in sync")
		return nil
	}

	logger.Infof("%d accounts removed, %d remain", removed, len(surviving))
	st.AccountKPs = surviving

	// Re-read RPCURL from the existing state file so ToPersistedState has it.
	oldPS, err := state.NewPersistedState(stateFile)
	if err != nil {
		return fmt.Errorf("re-read state file: %w", err)
	}
	ps, err := st.ToPersistedState(oldPS.RPCURL)
	if err != nil {
		return fmt.Errorf("build updated state: %w", err)
	}
	if err := ps.Save(stateFile); err != nil {
		return fmt.Errorf("save updated state: %w", err)
	}
	logger.Infof("updated %s", stateFile)
	return nil
}
