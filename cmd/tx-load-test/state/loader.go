package state

import (
	"context"
	"fmt"
	"os"
)

type RuntimePhase string

const (
	RuntimePhaseBench    RuntimePhase = "bench"
	RuntimePhaseTeardown RuntimePhase = "teardown"
	RuntimePhaseSync     RuntimePhase = "sync"
)

type LoadedRuntimeState struct {
	Persisted *PersistedState
	Live      *State
	RPCURL    string
}

func LoadExistingSetupState(
	ctx context.Context,
	stateFile string,
	feePayerSeed string,
	rpcURL string,
	networkPassphrase string,
) (*State, error) {
	if _, err := os.Stat(stateFile); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat state file %q: %w", stateFile, err)
	}

	ps, err := NewPersistedState(stateFile)
	if err != nil {
		return nil, fmt.Errorf("state file %q exists but cannot be loaded: %w", stateFile, err)
	}
	if err := ps.ValidateSetupConfig(networkPassphrase); err != nil {
		return nil, fmt.Errorf("validate existing state %q: %w", stateFile, err)
	}
	if err := ps.ValidateRPCNetwork(ctx, rpcURL); err != nil {
		return nil, fmt.Errorf("validate existing state network %q: %w", stateFile, err)
	}
	if feePayerSeed == "" {
		return nil, fmt.Errorf(
			"TX_LOAD_TEST_FEE_PAYER_SEED is required when re-running setup " +
				"with an existing state file")
	}

	st, err := FromPersistedState(ps, feePayerSeed, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("reconstruct state from %q: %w", stateFile, err)
	}
	return st, nil
}

func LoadRuntimeState(
	ctx context.Context,
	phase RuntimePhase,
	stateFile string,
	feePayerSeed string,
	rpcURL string,
) (*LoadedRuntimeState, error) {
	ps, err := NewPersistedState(stateFile)
	if err != nil {
		return nil, fmt.Errorf("load state file %q: %w%s", stateFile, err, runtimePhaseHint(phase))
	}
	if err := ps.ValidateRPCNetwork(ctx, rpcURL); err != nil {
		return nil, fmt.Errorf("validate state network: %w", err)
	}
	if feePayerSeed == "" {
		return nil, fmt.Errorf(
			"TX_LOAD_TEST_FEE_PAYER_SEED is required -- set it to the fee-payer secret key")
	}
	st, err := FromPersistedState(ps, feePayerSeed, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("reconstruct state: %w", err)
	}
	if rpcURL == "" {
		rpcURL = ps.RPCURL
	}
	return &LoadedRuntimeState{Persisted: ps, Live: st, RPCURL: rpcURL}, nil
}

func runtimePhaseHint(phase RuntimePhase) string {
	switch phase {
	case RuntimePhaseBench:
		return "  -- run 'setup' first"
	case RuntimePhaseTeardown:
		return "  -- nothing to tear down"
	default:
		return ""
	}
}
