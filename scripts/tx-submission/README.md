# tx-submission benchmark harvest tooling

Measures how long the stellar-rpc process spends inside one `sendTransaction`
request, for [stellar-rpc#869](https://github.com/stellar/stellar-rpc/issues/869).
The numbers come from RPC's own telemetry, not from client wall time.

Read [`.claude/docs/869-tx-submission-plan.md`](../../.claude/docs/869-tx-submission-plan.md)
first. It is the source of truth for the method, the environment, and the phase
matrix. These scripts only make the run loop repeatable.

## Scripts

| Script | Function |
|---|---|
| `run-phase.sh` | Runs the full loop for a phase. Per mode: restart stellar-rpc, run `tx-load-test bench`, scrape `/metrics`, copy the RPC log out of the container, harvest a summary. Writes one result bundle. |
| `harvest.py` | Turns one RPC log plus one scrape into `summary-<mode>.json`. Python 3 standard library only. |
| `test_harvest.py` | Fixture tests for `harvest.py`. `python3 test_harvest.py`, or `python3 -m unittest discover scripts/tx-submission/`. |

### What `harvest.py` computes

- **Handler distribution** (the headline). Joins each `starting JSONRPC request`
  log line to its `finished JSONRPC request` line on `req`, then keeps the pairs
  whose starting line carries `method=sendTransaction`. The finished line has no
  `method` field, so the join is what separates submissions from the poller's
  `getTransaction` traffic in the same log. Percentiles are nearest-rank over the
  exact per-request samples.
- **Core leg.** Per-`status` quantiles, `_sum` and `_count` from
  `soroban_rpc_txsub_submission_duration_seconds`.
- **Cross-check.** The same extraction from
  `soroban_rpc_json_rpc_request_duration_seconds{endpoint="sendTransaction"}`.
  It must agree with the log-derived handler numbers.
- **Mean residue.** Handler mean minus core-leg mean. Quantiles never subtract,
  so the summary holds no subtracted quantile.

Every duration in the summary is an integer count of nanoseconds under a `_ns`
key. Anomalies land in a `warnings` array and never fail the run silently; the
script exits non-zero only on input it cannot use.

## Prerequisites

- `FEE_PAYER` exported — the seed `tx-load-test setup` printed.
- `LOG_FORMAT="json"` appended to `/opt/stellar/stellar-rpc/etc/stellar-rpc.cfg`
  in the container, followed by a restart. Without it `harvest.py` refuses the log.
- The quickstart container started with `--enable-stellar-rpc-admin-endpoint` and
  port 6061 published.
- `tx-load-test` built at the repo root: `go build -o tx-load-test ./cmd/tx-load-test/`.

## Phase A — standalone, laptop

10 RPS for 120 s per mode against the local quickstart.

```bash
export FEE_PAYER="S..."
./scripts/tx-submission/run-phase.sh \
  --out txsub-standalone-$(date -u +%Y%m%d) \
  --target-rps 10 --duration 120s --ramp-up 10s
```

## Phase B — testnet, 2x box

2 RPS for 60 s per mode, against the testnet state pool.

```bash
export FEE_PAYER="S..."
HEALTH_TIMEOUT_SECONDS=600 ./scripts/tx-submission/run-phase.sh \
  --out txsub-testnet-$(date -u +%Y%m%d) \
  --target-rps 2 --duration 60s --ramp-up 10s \
  --state-file state-testnet.json
```

`HEALTH_TIMEOUT_SECONDS` is raised on testnet because each between-mode RPC
restart makes the captive core catch up to the network again before RPC
reports healthy.

Run `restore --mode all --verify --state-file state-testnet.json` first if the
pool has been idle.

`bench` reads its RPC URL from the state file. `--rpc-url` only drives the
getHealth poll between modes, so point both at the same endpoint.

## Result bundle

```
txsub-<network>-<yyyymmdd>/
  meta.json            # host, container, image, RPC version banner, resolved flags
  client-<mode>.ndjson # tx-load-test client metrics
  metrics-<mode>.prom  # /metrics scrape taken right after the run
  rpc-log-<mode>.log   # the RPC log copied out of the container
  summary-<mode>.json  # harvest.py output
```

`run-phase.sh` writes nothing outside `--out`. Upload finished bundles to
`gs://rpc-full-history/results/`; Marwen runs the upload.

## Running `harvest.py` on its own

Re-harvest an existing bundle without re-running the benchmark:

```bash
python3 scripts/tx-submission/harvest.py \
  --log    txsub-standalone-20260728/rpc-log-sac-transfer.log \
  --metrics txsub-standalone-20260728/metrics-sac-transfer.prom \
  --mode sac-transfer \
  --window-start 2026-07-28T10:00:00Z --window-end 2026-07-28T10:02:10Z \
  --expected-count 1203 \
  --out txsub-standalone-20260728/summary-sac-transfer.json
```

The window bounds are inclusive and filter on the finished-line timestamp.
`--expected-count` is `submission_submitted` for the mode's workload in
`client-<mode>.ndjson`; a mismatch becomes a warning. `run-phase.sh` fills all
three automatically.
