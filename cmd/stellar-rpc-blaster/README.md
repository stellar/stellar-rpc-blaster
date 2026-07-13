# stellar-rpc-blaster

CLI load testing tool for [Stellar RPC](https://developers.stellar.org/docs/data/rpc). Blasts configurable traffic at one or more RPC endpoints, ramps load with a stepped pacer, and produces a JSON results file with per-step-interval latency and error breakdowns.

Two modes:
- **`run`** — execute a load test against a live RPC instance
- **`generate`** — collect seed data (transaction hashes, ledger keys, contract events) from an RPC for data-dependent endpoints

## Quick Start

```bash
# 1. Build
make build-rpc-blaster

# 2. Generate seed data (only needed for data-dependent endpoints)
./stellar-rpc-blaster generate --rpc-url http://127.0.0.1:8000

# 3. Run a load test
./stellar-rpc-blaster run \
  --rpc-url http://127.0.0.1:8000 \
  --config-path ./cmd/stellar-rpc-blaster/internal/config/config.example.toml \
  --duration 60s \
  --ramp-up 30s
```

Results are written to `./cmd/stellar-rpc-blaster/output/test-results-<timestamp>.json`.

## Build

```bash
make build-rpc-blaster          # compile binary with version info from git
make test                       # run tests
```

Requires Go 1.25+ and `jq`.

## Commands

### `run`

Execute a load test.

| Flag | Type | Default | Env Var | Description |
|------|------|---------|---------|-------------|
| `--rpc-url` | string | *(required)* | `RPC_URL` | Target RPC server URL |
| `--config-path` | string | *(required)* | `CONFIG_PATH` | Path to TOML config file |
| `--duration` | duration | *(required)* | `DURATION` | Test duration (e.g. `60s`, `5m`) |
| `--ramp-up` | duration | `0` | `RAMP_UP` | Ramp-up period before reaching target RPS |
| `--step-interval` | duration | `5s` | `STEP_INTERVAL` | Time between RPS step increases during ramp |
| `--test-output-path` | string | `./cmd/stellar-rpc-blaster/output` | `TEST_OUTPUT_PATH` | Base directory for results JSON |
| `--input-data-path` | string | — | `INPUT_DATA_PATH` | Path to seed data file (from `generate`) |
| `--serial` | bool | `false` | `SERIAL` | Run endpoints one at a time instead of concurrently |

### `generate`

Collect seed data from an RPC instance for use with data-dependent endpoints.

| Flag | Type | Default | Env Var | Description |
|------|------|---------|---------|-------------|
| `--rpc-url` | string | *(required)* | `RPC_URL` | Target RPC server URL |
| `--output` | string | `./output/seed.json` | `OUTPUT` | Output path for seed data |
| `--ledger-window` | string | — | `LEDGER_WINDOW` | Ledger range as `START[,END]` to sample from |
| `--count` | uint32 | `5000` | `COUNT` | Number of ledgers to sample |

**Ledger window behavior:**
- Omitted: `[LatestCheckpoint - count, LatestCheckpoint]`
- `START` only: `[START, LatestCheckpoint]`
- `START,END`: `[START, END]`
- If the window is larger than `count`, ledgers are uniformly sampled across the range

## Configuration

Endpoints are configured in a TOML file. Each endpoint block sets the target RPS and an optional starting RPS for ramp-up:

```toml
# Path to seed data (required for data-dependent endpoints)
input_data_path = "./output/seed.json"

[endpoints.getHealth]
rps = 100

[endpoints.getLedgers]
rps = 50
start_rps = 10
```

### Supported Endpoints

**No-parameter endpoints** (no seed data needed):

| Endpoint | Description |
|----------|-------------|
| `getHealth` | Server health check |
| `getNetwork` | Network passphrase and protocol info |
| `getVersionInfo` | Server version and build info |
| `getLatestLedger` | Latest closed ledger |
| `getFeeStats` | Fee statistics |

**Data-dependent endpoints** (require seed data from `generate`):

| Endpoint | Seed Data Used |
|----------|---------------|
| `getTransaction` | Transaction hashes |
| `simulateTransaction` | Transaction hashes |
| `getLedgerEntries` | Ledger keys |
| `getTransactions` | Ledger ranges |
| `getLedgers` | Ledger ranges |
| `getEvents` | Contract IDs, topics, parameters |

### Validation Rules

- `step-interval` must be a positive multiple of 5s to align with the live logs
- `step-interval` cannot exceed `ramp-up`
- At least one endpoint must have `rps > 0`
- `start_rps` must be less than or equal to `rps` when set
- If any endpoint sets `start_rps`, `--ramp-up` must be provided
- Data-dependent endpoints require `input_data_path`

## Run Modes

### Concurrent (default)

All configured endpoints blast simultaneously for the full `--duration`.

### Serial (`--serial`)

Endpoints run one at a time. Each gets the full `--duration`, so total test time is `duration * number_of_endpoints`. Useful for isolating per-endpoint behavior without cross-endpoint interference.

## Ramp-Up and Stepped Pacer

When `--ramp-up` is set, the pacer steps RPS from a starting rate to the target over the ramp period, then holds constant at the target for the remaining duration.

The step interval (default 5s) controls how frequently the rate increases. Each step adds a fixed increment:

```
stepSize = (rps - start_rps) / steps
```

**`start_rps` behavior:**
- **Omitted** — auto-calculated to the first step value, so no time is spent at 0 RPS
- **Explicit `0`** — truly starts at 0 RPS (no requests during the first step)
- **Explicit `N`** — starts at exactly N RPS

**Example:** `rps = 50`, `start_rps = 10`, `--ramp-up 30s`, `--step-interval 5s`

```
Steps: 6   StepSize: (50 - 10) / 6 ≈ 6.67 RPS/step

 0-5s:   10.0 RPS
 5-10s:  16.7 RPS
10-15s:  23.3 RPS
15-20s:  30.0 RPS
20-25s:  36.7 RPS
25-30s:  43.3 RPS
30s+:    50.0 RPS (constant)
```

## Seed Data Generation

The `generate` command samples ledgers from a live RPC and extracts:

- **Transaction hashes** — used by `getTransaction` and `simulateTransaction`
- **Ledger keys** — XDR-encoded keys used by `getLedgerEntries`
- **Contract events** — contract IDs, event topics, and parameters used by `getEvents`

This ensures load test requests hit realistic, existing on-chain data rather than synthetic inputs that may short-circuit server processing.

## Output and Results

Results are written to `./output/test-results-<timestamp>.json`:

```json
{
  "start": "2026-04-02T21:07:45Z",
  "end": "2026-04-02T21:11:45Z",
  "duration_seconds": 240,
  "endpoints": {
    "getLedgers": {
      "total_requests": 6749,
      "success": 1332,
      "errors": 5417,
      "target_rps": 50,
      "percentiles_ms": {
        "p50.0": 15007.74,
        "p95.0": 15007.74,
        "p99.0": 15007.74,
        "p99.9": 15015.94
      },
      "error_types": {
        "context deadline exceeded (Client.Timeout exceeded while awaiting headers)": {
          "error_msg": "...",
          "error_code": 0,
          "count": 5417,
          "time_first_seen": "2026-04-02T17:09:28Z",
          "time_last_seen": "2026-04-02T17:11:45Z"
        }
      },
      "timeline": [
        {
          "target_rps": 7.14, "success": 213, "errors": 0, "error_rate_pct": 0,
          "p50_ms": 98.3, "p95_ms": 320.5, "p99_ms": 514.3, "p99.9_ms": 514.3
        },
        {
          "target_rps": 14.29, "success": 424, "errors": 0, "error_rate_pct": 0,
          "p50_ms": 210.4, "p95_ms": 650.2, "p99_ms": 898.6, "p99.9_ms": 898.6
        },
        {
          "target_rps": 21.43, "success": 454, "errors": 0, "error_rate_pct": 0,
          "p50_ms": 2150.4, "p95_ms": 7200.0, "p99_ms": 8355.8, "p99.9_ms": 8355.8
        },
        {
          "target_rps": 28.57, "success": 241, "errors": 382, "error_rate_pct": 61.32,
          "p50_ms": 15007.7, "p95_ms": 15007.7, "p99_ms": 15007.7, "p99.9_ms": 15007.7
        }
      ]
    }
  }
}
```

### Key fields

| Field | Description |
|-------|-------------|
| `total_requests` | Total requests sent to this endpoint |
| `success` / `errors` | Count of successful (2xx + no JSON-RPC error) vs failed requests |
| `target_rps` | Final target RPS for this endpoint |
| `percentiles_ms` | Overall latency percentiles across the full test (p50, p95, p99, p99.9) |
| `error_types` | Errors grouped by message, with count and first/last seen timestamps |
| `timeline` | Per-step-interval snapshots showing how the endpoint performed over time |

### Timeline snapshots

Each timeline entry corresponds to one step interval (default 5s) and contains:

| Field | Description |
|-------|-------------|
| `target_rps` | Target RPS during this window |
| `success` / `errors` | Request counts in this window |
| `error_rate_pct` | Error rate as a percentage (e.g. `4.52` = 4.52%) |
| `p50_ms` ... `p99.9_ms` | Latency percentiles for this window only |

The timeline reveals the degradation curve: scan for where `error_rate_pct` jumps from 0 and where `p99_ms` spikes — that's the RPS at which the RPC buckled.

---

> **Note:** This repository is not in scope for the Stellar Development Foundation bug bounty program. Vulnerabilities found in this repo are not eligible for rewards.