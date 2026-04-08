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
	resetSeq SequenceResetFunc,
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
	numPollWorkers, pollWg := startPollWorkers(ctx, logger, cfg, rpc, state, resetSeq)

	var metrics vegeta.Metrics

	attacker := vegeta.NewAttacker(vegeta.Client(httpClient))
	results := attacker.Attack(targeter, pacer, cfg.Duration, label)

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
			processAttackResult(res, &metrics, logger, state, resetSeq)
		}
	}

	drainAttackResults(results, &metrics, logger, state, resetSeq)

	close(state.hashes)
	metrics.Close()

	submitted, httpErr, queued, tryAgainLater, submitErrors := state.submissionSnapshot()
	logger.Infof("attack complete  -- submitted=%d httpErr=%d queued=%d tryAgainLater=%d submitErrors=%d  -- waiting for poll workers",
		submitted, httpErr, queued, tryAgainLater, submitErrors)
	if submitErrors > 0 {
		state.errorCodes.log(logger)
	}

	waitForPollWorkers(logger, queued, numPollWorkers, pollWg, state)

	included, onChainFail, pollErr := state.pollSnapshot()
	logger.Infof("on-chain results  -- included=%d failed=%d pollErr=%d",
		included, onChainFail, pollErr)

	logE2ELatencies(logger, state.e2eStats.snapshot(), pollErr)
	logVegetaMetrics(logger, metrics)

	return nil
}
