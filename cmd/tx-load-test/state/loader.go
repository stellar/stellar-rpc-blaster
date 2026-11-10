package state

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type RuntimePhase string

const (
	RuntimePhaseBench     RuntimePhase = "bench"
	RuntimePhaseRestore   RuntimePhase = "restore"
	RuntimePhaseTeardown  RuntimePhase = "teardown"
	RuntimePhaseSync      RuntimePhase = "sync"
	RuntimePhaseExtendTTL RuntimePhase = "extend-ttl"
)

type LoadedRuntimeState struct {
	Persisted *PersistedState
	Live      *State
	RPCURL    string
}

const DefaultRuntimeAccountPreflightSampleSize = 10

type RuntimeLoadOptions struct {
	VerifyAccountsExist bool
	AccountCheckSample  int
}

func (o RuntimeLoadOptions) normalizedSample() int {
	if o.AccountCheckSample > 0 {
		return o.AccountCheckSample
	}
	return DefaultRuntimeAccountPreflightSampleSize
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
			"FEE_PAYER is required when re-running setup " +
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
	return LoadRuntimeStateWithOptions(ctx, phase, stateFile, feePayerSeed, rpcURL, RuntimeLoadOptions{})
}

func LoadRuntimeStateWithOptions(
	ctx context.Context,
	phase RuntimePhase,
	stateFile string,
	feePayerSeed string,
	rpcURL string,
	options RuntimeLoadOptions,
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
			"FEE_PAYER is required -- set it to the fee-payer secret key")
	}
	st, err := FromPersistedState(ps, feePayerSeed, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("reconstruct state: %w", err)
	}
	if rpcURL == "" {
		rpcURL = ps.RPCURL
	}
	if options.VerifyAccountsExist {
		if err := validateRuntimeAccountsExist(ctx, st, options.normalizedSample(), func(ctx context.Context, address string) (bool, error) {
			return AccountExists(ctx, st.RPCClient, address)
		}); err != nil {
			return nil, fmt.Errorf("runtime account preflight: %w", err)
		}
	}
	return &LoadedRuntimeState{Persisted: ps, Live: st, RPCURL: rpcURL}, nil
}

func validateRuntimeAccountsExist(
	ctx context.Context,
	st *State,
	sampleSize int,
	exists func(context.Context, string) (bool, error),
) error {
	if st == nil || len(st.AccountKPs) == 0 || exists == nil {
		return nil
	}
	limit := min(sampleSize, len(st.AccountKPs))
	missingCount := 0
	examples := make([]string, 0, 5)
	for i := 0; i < limit; i++ {
		kp := st.AccountKPs[i]
		if kp == nil {
			continue
		}
		found, err := exists(ctx, kp.Address())
		if err != nil {
			return fmt.Errorf("check account %s: %w", kp.Address(), err)
		}
		if found {
			continue
		}
		missingCount++
		if len(examples) < cap(examples) {
			examples = append(examples, kp.Address())
		}
	}
	if missingCount == 0 {
		return nil
	}
	return fmt.Errorf(
		"%d of the first %d participant accounts are missing on-chain; examples: %s -- run 'sync', disable this preflight with --skip-account-preflight, or rerun setup",
		missingCount,
		limit,
		strings.Join(examples, ", "),
	)
}

func runtimePhaseHint(phase RuntimePhase) string {
	switch phase {
	case RuntimePhaseBench:
		return "  -- run 'setup' first"
	case RuntimePhaseRestore, RuntimePhaseExtendTTL:
		return "  -- run 'setup' first"
	case RuntimePhaseTeardown:
		return "  -- nothing to tear down"
	default:
		return ""
	}
}
