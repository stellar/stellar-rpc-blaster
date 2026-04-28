package benchmark

import (
	"context"
	"net/http"
	"time"

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
	newTargeter func(context.Context) (vegeta.Targeter, error),
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

	targeter, err := newTargeter(attackCtx)
	if err != nil {
		return err
	}

	maxTx := cfg.TargetRPS*int(cfg.Duration.Seconds()) + 1000
	state := newAttackState(maxTx)
	numPollWorkers, pollWg := startPollWorkersWithTrace(ctx, logger, cfg, rpc, state, accounts, label, recorder)

	var metrics vegeta.Metrics
	attackStartedAt := time.Now()
	progressTicker := time.NewTicker(43 * time.Second)
	defer progressTicker.Stop()

	attacker := vegeta.NewAttacker(vegeta.Client(httpClient))
	results := attacker.Attack(traceTargeter(label, targeter, recorder), pacer, cfg.Duration, label)

loop:
	for {
		select {
		case <-attackCtx.Done():
			attacker.Stop()
			break loop
		case <-progressTicker.C:
			submitted, httpErr, queued, tryAgainLater, submitErrors, ambiguous := state.submissionSnapshot()
			logger.Infof("attack progress -- elapsed=%s submitted=%d httpErr=%d queued=%d tryAgainLater=%d submitErrors=%d ambiguous=%d",
				time.Since(attackStartedAt).Round(time.Second), submitted, httpErr, queued, tryAgainLater, submitErrors, ambiguous)
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
	logLedgerMetrics(logger, state.ledgerStats.snapshot())
	logVegetaMetrics(logger, metrics)

	return nil
}
