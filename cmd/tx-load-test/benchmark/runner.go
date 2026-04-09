package benchmark

import (
	"context"
	"net/http"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/support/log"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/internal/run/engine"
)

// runVegetaAttack is the shared Vegeta harness used by all benchmark modes.
// It constructs a RampToConstantPacer and drives the attack for cfg.Duration.
//
// After the attack finishes, a pool of poll worker goroutines drains the
// accepted-transaction hashes by calling getTransaction until each reaches a
// terminal state (SUCCESS or FAILED).  This gives a complete picture of
// on-chain outcomes, not just submission acceptance.
func runVegetaAttack(
	ctx context.Context,
	logger *log.Entry,
	cfg config.Config,
	httpClient *http.Client,
	rpc *rpcclient.Client,
	label string,
	targeter vegeta.Targeter,
	accounts accountLeaseManager,
	recorder *benchmarkTraceRecorder,
) error {
	pacer := engine.RampToConstantPacer{
		StartRPS:      1,
		MaxRPS:        cfg.TargetRPS,
		RampDuration:  cfg.RampUp,
		TotalDuration: cfg.Duration,
	}

	attackCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	maxTx := cfg.TargetRPS*int(cfg.Duration.Seconds()) + 1000
	state := newAttackState(maxTx)
	numPollWorkers, pollWg := startPollWorkersWithTrace(ctx, logger, cfg, rpc, state, accounts, label, recorder)

	var metrics vegeta.Metrics

	attacker := vegeta.NewAttacker(vegeta.Client(httpClient))
	results := attacker.Attack(traceTargeter(label, targeter, recorder), pacer, cfg.Duration, label)

loop:
	for {
		select {
		case <-attackCtx.Done():
			attacker.Stop()
			break loop
		case res, ok := <-results:
			if !ok {
				break loop
			}
			processAttackResult(res, &metrics, logger, state, accounts, label, recorder)
		}
	}

	drainAttackResults(results, &metrics, logger, state, accounts, label, recorder)

	close(state.hashes)
	metrics.Close()

	submitted, httpErr, queued, tryAgainLater, submitErrors, ambiguous := state.submissionSnapshot()
	logger.Infof("attack complete  -- submitted=%d httpErr=%d queued=%d tryAgainLater=%d submitErrors=%d ambiguous=%d  -- waiting for poll workers",
		submitted, httpErr, queued, tryAgainLater, submitErrors, ambiguous)
	if submitErrors > 0 {
		state.errorCodes.log(logger, "submit ERROR breakdown")
		state.submitOpResults.log(logger, "submit ERROR op-result breakdown")
		state.submitDiagnostics.log(logger, "submit diagnostic summary")
	}

	waitForPollWorkers(logger, queued, numPollWorkers, pollWg, state)

	included, onChainFail, pollErr := state.pollSnapshot()
	logger.Infof("on-chain results  -- included=%d failed=%d pollErr=%d",
		included, onChainFail, pollErr)
	if onChainFail > 0 {
		state.onChainErrorCodes.log(logger, "on-chain failure breakdown")
		state.onChainOpResults.log(logger, "on-chain failure op-result breakdown")
		state.onChainDiagnostics.log(logger, "on-chain diagnostic summary")
	}

	logE2ELatencies(logger, state.e2eStats.snapshot(), pollErr)
	logVegetaMetrics(logger, metrics)

	return nil
}
