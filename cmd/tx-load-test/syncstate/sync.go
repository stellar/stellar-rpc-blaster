// Package syncstate reconciles the local state file with on-chain account
// existence.
package syncstate

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/support/log"
	"golang.org/x/sync/errgroup"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

const accountCheckConcurrency = 32

// SyncState checks which participant accounts still exist on-chain
// and rewrites the state file with only those that do. This reconciles the
// local state file with the actual network state.
func SyncState(ctx context.Context, logger *log.Entry, st *state.State, stateFile, rpcURL string) error {
	logger = logger.WithField("phase", "sync")

	if len(st.AccountKPs) == 0 {
		logger.Info("no participant accounts in state  -- nothing to sync")
		return nil
	}

	workerLimit := min(accountCheckConcurrency, len(st.AccountKPs))
	logger.Infof("checking %d accounts with up to %d concurrent workers", len(st.AccountKPs), workerLimit)

	existsByIndex := make([]bool, len(st.AccountKPs))
	g, groupCtx := errgroup.WithContext(ctx)
	g.SetLimit(workerLimit)
	for i, kp := range st.AccountKPs {
		if kp == nil {
			continue
		}
		i, kp := i, kp
		g.Go(func() error {
			exists, err := state.AccountExists(groupCtx, st.RPCClient, kp.Address())
			if err != nil {
				return fmt.Errorf("check account %s: %w", kp.Address(), err)
			}
			existsByIndex[i] = exists
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	var surviving []*keypair.Full
	var removed int
	for i, kp := range st.AccountKPs {
		if kp == nil {
			continue
		}
		if existsByIndex[i] {
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

	if len(st.SACHolderKPs) > 0 {
		survivingSet := make(map[string]struct{}, len(surviving))
		for _, kp := range surviving {
			survivingSet[kp.Address()] = struct{}{}
		}
		var survivingHolders []*keypair.Full
		for _, kp := range st.SACHolderKPs {
			if _, ok := survivingSet[kp.Address()]; ok {
				survivingHolders = append(survivingHolders, kp)
			}
		}
		st.SACHolderKPs = survivingHolders
	}

	ps, err := st.ToPersistedState(rpcURL)
	if err != nil {
		return fmt.Errorf("build updated state: %w", err)
	}
	if err := ps.Save(stateFile); err != nil {
		return fmt.Errorf("save updated state: %w", err)
	}
	logger.Infof("updated %s", stateFile)
	return nil
}
