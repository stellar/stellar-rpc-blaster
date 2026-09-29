# tx-load-test

A standalone Stellar Soroban RPC load-testing tool. It drives sustained transaction traffic against an RPC endpoint, measuring submission latency, acceptance rates, and on-chain inclusion outcomes.

## Three-Phase Design

The tool is split into three independent phases connected by a JSON state file:

```
setup  -->  state.json  -->  restore  -->  bench  (repeatable)
                        -->  teardown
```

- **`setup`** -- one-time ledger initialization: creates accounts, assets, trustlines, SAC contracts, Soroswap pools/liquidity, and the OZ benchmark token. Re-running with a higher `--accounts` value adds accounts incrementally.
- **`restore`** -- optional pre-benchmark maintenance: simulates benchmark-shaped probes, restores archived Soroban state, and logs per-mode restore summaries without running benchmark traffic.
- **`bench`** -- drives load against the RPC endpoint using pre-built state. Run as many times as needed.
- **`teardown`** -- merges all participant accounts back into the fee payer, recovering XLM. Deletes the state file on success.
- **`sync`** -- reconciles the state file with on-chain reality (removes accounts that no longer exist).

This separation means setup's expensive on-chain work is done once, benchmarks can be iterated quickly, and cleanup is explicit.

## Prerequisites

- Go 1.25 or newer.
- A Stellar RPC endpoint for the target network.
- A fee-payer seed in `FEE_PAYER` for `bench`, `teardown`, and `sync`. For `setup` on testnet/futurenet, `FEE_PAYER` may be omitted and the tool will generate/fund a temporary fee payer via friendbot.
- For `setup` on testnet/mainnet, Soroswap factory and router contract IDs must be supplied with `--soroswap-factory` and `--soroswap-router`. On standalone/futurenet, setup can auto-deploy Soroswap contracts from the local Wasm artifacts under `contracts/`.
- `jq` is optional, but useful for inspecting the generated NDJSON metrics and trace files.

## Quick Start

```bash
# Build
go build -o tx-load-test ./cmd/tx-load-test/

# 1. Set up ledger state (testnet, 3000 accounts)
export FEE_PAYER="S..."  # optional; omit to auto-generate via friendbot
./tx-load-test setup --rpc-url https://soroban-testnet.stellar.org --network testnet --accounts 3000

# 2. If the state has been idle, dry-run restore first (requires the same fee-payer seed used for setup)
export FEE_PAYER="S..."
./tx-load-test restore --mode all --dry-run

# 3. Restore archived state if the dry-run reports restore-needed probes
./tx-load-test restore --mode all --verify

# 4. Run a benchmark
./tx-load-test bench --mode sac-transfer --target-rps 300 --duration 60s
# Writes flattened metrics to metrics/tx-load-test-metrics-<timestamp>-sac-transfer.ndjson by default.

# 5. Need more accounts? Just re-run setup with a higher target.
./tx-load-test setup --rpc-url https://soroban-testnet.stellar.org --network testnet --accounts 5000

# 6. Clean up (also requires FEE_PAYER)
./tx-load-test teardown
```

## Commands

### `setup`

Creates all required ledger state and writes `state.json`. If a state file already exists, loads it and only creates the additional accounts needed to reach the `--accounts` target.

| Flag | Default | Description |
|---|---|---|
| `--rpc-url` | *(required)* | Stellar RPC HTTP endpoint |
| `--network` | *(required)* | Network shorthand: `testnet`, `futurenet`, `mainnet`, `standalone` |
| `--duration` | `100s` | Planned benchmark duration used when sizing account partitions |
| `--target-rps` | `50` | Planned Soroban steady-state requests per second used when sizing account partitions |
| `--classic-rps` | `100` | Planned simple-payment steady-state transactions per second used when sizing account pools for the superset setup shape; `0` disables the companion stream; each transaction carries one payment op |
| `--soroswap-factory` | *(required on `testnet`/`mainnet`)* | Soroswap factory contract ID; optional on `standalone`/`futurenet`, where setup can auto-deploy it |
| `--soroswap-router` | *(required on `testnet`/`mainnet`)* | Soroswap router contract ID; optional on `standalone`/`futurenet`, where setup can auto-deploy it |
| `--liquidity-per-pool` | `1000000` | Token units of each asset to seed into each Soroswap benchmark pool |
| `--accounts` | `5000` | Target number of participant accounts |
| `--base-reserve-xlm` | `3.0` | XLM to fund each account; covers the three-trustline holder reserve plus fee/headroom |
| `--state-file` | `state.json` | Output state file path |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

The fee-payer seed is read from the `FEE_PAYER` environment variable. If unset, a temporary keypair is generated and funded via friendbot (testnet/futurenet only).

The `--base-reserve-xlm` default is intentionally conservative. On current public networks the Stellar base reserve is 0.5 XLM, and a benchmark holder account needs three classic asset trustlines (`BLTA`, `BLTB`, `BLTC`). Stellar minimum balance is `(2 + subentry count) * base reserve`, so a holder requires `(2 account reserves + 3 trustline reserves) * 0.5 = 2.5 XLM`. Funding each account with 3.0 XLM leaves about 0.5 XLM per holder for small incidental fees, append/repair operations, and reserve-parameter headroom. Passive accounts do not need the trustline reserves, but setup uses one funding amount for all participant accounts so every account can be promoted to the holder subset later.

If `setup` is re-run against an existing `state.json`, `FEE_PAYER` must be set and must match the hash recorded in the state file.
Re-running `setup` requires the resolved network passphrase to match the value already recorded in `state.json`. The `--rpc-url` may change, but the chosen endpoint must report that same passphrase via `getNetwork`.

Setup now provisions the benchmark superset once so the resulting ledger state can run `sac-transfer`, `oz-transfer`, or `soroswap` without re-running setup. That means setup always validates the requested `--accounts`, `--target-rps`, `--classic-rps`, and `--duration` against all benchmark modes, always provisions the required trustlined holder subset, and always prepares Soroswap state. On testnet/mainnet, `--soroswap-factory` and `--soroswap-router` are therefore required. On standalone and futurenet, setup can auto-upload and deploy deterministic Soroswap pair/factory/router contracts from the local Wasm artifacts under `contracts/` when those flags are omitted. In both cases setup validates that the router points at the resolved factory, creates or reuses the benchmark pair contracts for the benchmark SAC assets, and seeds empty benchmark pools with `--liquidity-per-pool` units of each asset. Run `contracts/update-wasms.sh` to refresh the local OZ and Soroswap artifacts.

**Setup steps (in order):**
1. **Fee payer** -- verify/create/fund the fee-payer account. Auto-tops-up via friendbot if balance is insufficient.
2. **Assets** -- register 3 benchmark classic assets (BLTA, BLTB, BLTC) with the fee payer as issuer.
3. **Accounts** -- derive keypairs deterministically from the fee-payer seed. If a state file was loaded, only the delta accounts are created. A formula-derived prefix of the participant set is provisioned as the trustlined holder superset required to support every benchmark mode for the requested rates and duration: those accounts are created in batches of 19 (CreateAccount + 3 ChangeTrust, capped at 20 signatures) and minted in batches of 33. Remaining accounts are created as XLM-only participants.
4. **SAC** -- deploy a Stellar Asset Contract for each of the 3 assets (idempotent; skips if already deployed).
5. **Soroswap core** -- resolve factory/router contract IDs. On standalone/futurenet this can auto-upload and deploy deterministic Soroswap core contracts from the flat local Wasm artifacts in `contracts/`.
6. **Soroswap pairs** -- validate the resolved factory/router contracts and create or reuse the benchmark pair contracts.
7. **Soroswap liquidity** -- seed each empty benchmark pair through the router using the fee payer as the initial LP. Re-running setup skips pools that already have liquidity.
8. **OZ token** -- upload and deploy the upgradeable OpenZeppelin benchmark token, then mint balances to participant accounts in batches.

If setup is interrupted, a best-effort cleanup merges whatever accounts exist and writes partial state so `teardown` can finish later.

**Incremental setup example:**
```bash
# Start with 2000 accounts
./tx-load-test setup --rpc-url https://... --network testnet --accounts 2000

# Later, expand to 5000 -- only accounts 2001-5000 are created
./tx-load-test setup --rpc-url https://... --network testnet --accounts 5000
```

### `restore`

Restores archived Soroban state before a benchmark run. This command is meant for state files that sit idle between infrequent tests, especially `oz-transfer` and `soroswap`, where account-specific contract data can archive.

`restore` uses simulation only inside this maintenance command. It does not submit the benchmark transfer/swap invokes used as probes; it submits only `RestoreFootprint` transactions when simulation reports archived state. For `soroswap`, the command also runs a secondary benchmark-footprint validation pass: it builds the same rewritten footprints used by the benchmark targeter, compares them with fresh simulation footprints, and fails on missing or unexpected contract-data keys. The hot benchmark path remains simulation-free per generated request.

| Flag | Default | Description |
|---|---|---|
| `--mode` | `all` | Restore scope: `all`, `sac-transfer`, `oz-transfer`, or `soroswap` |
| `--dry-run` | `false` | Simulate and log what would need restore; submit no restore transactions |
| `--verify` | `false` | After restore, re-run the selected probes and fail if any still require restore |
| `--account-start` | `0` | 0-based offset into the selected participant account list |
| `--account-limit` | `0` | Maximum selected accounts per applicable mode; `0` means all remaining accounts |
| `--progress-interval` | `100` | Log restore progress every N probes; set negative to disable periodic progress |
| `--rpc-url` | *(from state file)* | Override the RPC URL stored in `state.json` |
| `--state-file` | `state.json` | Input state file path |
| `--skip-account-preflight` | `false` | Skip the sampled on-chain participant-account existence check before restore starts |
| `--account-preflight-sample` | `10` | Number of participant accounts to sample during runtime preflight |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

Example workflow:

```bash
export FEE_PAYER="S..."

# See how much archived state would need restoring.
./tx-load-test restore --mode all --dry-run

# Restore and verify the selected probes are live afterwards.
./tx-load-test restore --mode all --verify

# For large states, restore in chunks.
./tx-load-test restore --mode oz-transfer --account-start 0 --account-limit 500 --verify
./tx-load-test restore --mode oz-transfer --account-start 500 --account-limit 500 --verify
```

Each mode logs a start message, an update after the first probe, periodic progress every `--progress-interval` probes, a final progress update, and a summary. Progress and summary logs include probes simulated, restore-needed probes, restore transactions submitted, no-op probes, restored read-only/read-write key counts, selected account range, selected account count, elapsed time, and the first few errors. In `--dry-run`, summaries use “would restore” wording and `restoreTransactions=0`. Soroswap additionally logs `soroswap benchmark footprint validation summary` with template/probe counts, restore-needed validation probes, footprint mismatches, missing keys, unexpected keys, and allowed extra read-write trustline keys.

`--account-limit` limits selected accounts, not total probe count. `sac-transfer` runs one representative transfer probe per selected holder account per SAC contract. `oz-transfer` runs one restore probe per selected account. `soroswap` runs four probes per selected holder account because it checks two benchmark pools in both swap directions. With `--mode all --account-limit 1000`, the command can therefore run up to 3000 SAC probes, 1000 OZ probes, and 4000 Soroswap probes.

Mode-specific scope:
- **`sac-transfer`** runs representative transfer probes from each selected holder account against the three SAC contracts so simulation can restore both SAC WASM/code and contract instance state, plus any account-specific SAC contract data a transfer simulation reports. Participant balances are classic trustlines and are not Soroban archived state.
- **`oz-transfer`** probes selected participant accounts so each selected account's OZ `Balance` contract data is touched at least once.
- **`soroswap`** probes selected holder accounts across the benchmark pools and both swap directions, covering shared router/pair/pool state and account-specific trader contract data.

### `extend-ttl`

Extends the TTL of every Soroban ledger entry a benchmark run can touch, so state never crosses the archival boundary between runs. This exists because several footprint entries are extended by *nothing* at invocation time — the OZ token instance (the OpenZeppelin library defines instance-TTL constants but never calls extend), the OZ Wasm code entry (code entries cannot be self-extended by contract code), and the SAC instances (the host's SAC implementation only extends balance/allowance entries) — so they archive on a fixed calendar regardless of run cadence. The remaining entries (OZ balances, pair instances, pair SAC fund balances) are extended on touch, but only by ~30 days, so an idle gap longer than that archives the whole hot set at once.

The entry set is enumerated deterministically from the state file plus the `FEE_PAYER`-derived account pool: all contract instances (OZ token, SACs, soroswap router/factory/pairs), their Wasm code entries (discovered from the fetched instances), the pairs' SAC fund balances, and the OZ `Balance` entry of every pool account. The chain is consulted only for current TTLs. Entries already live past the target are skipped; already-archived entries cannot be extended — the command reports them and fails unless `--restore-archived` is set, in which case it first submits `RestoreFootprint` transactions bringing them back at the ~120-day network minimum TTL, then extends everything to the target. On protocol 23 this is the reliable way to heal archived state: the `restore` subcommand's simulation probes classify every archived entry as autorestore-class and submit nothing, leaving inline restoration (and its rent) to the next bench's transactions.

| Flag | Default | Description |
|---|---|---|
| `--extend-to-days` | `180` | Target TTL in days from now, clamped to the network `maxEntryTTL` (~180 days on mainnet); entries already live past this are skipped |
| `--batch-size` | `25` | Read-only footprint keys per `ExtendFootprintTtl` transaction |
| `--dry-run` | `false` | Classify and report entry TTLs, and simulate every would-be batch to report a total cost estimate; submit nothing |
| `--restore-archived` | `false` | Restore already-archived entries (`RestoreFootprint`, back to the ~120-day minimum TTL) before extending, instead of failing on them |
| `--skip-balances` | `false` | Extend only the infra set (instances, wasm, pair funds); see cost note below |
| `--rpc-url` | *(from state file)* | Override the RPC URL stored in `state.json` |
| `--state-file` | `state.json` | Input state file path |
| `--skip-account-preflight` | `false` | Skip the sampled on-chain participant-account existence check |
| `--account-preflight-sample` | `10` | Number of participant accounts to sample during runtime preflight |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

Example workflow:

```bash
export FEE_PAYER="S..."

# Inspect current TTLs, see what would be extended, and get a cost estimate
# (the dry run simulates every batch, so the estimate is the RPC's own rent
# math -- expect it to take about as long as the real pass's simulations).
./tx-load-test extend-ttl --dry-run

# Extend everything below the target to ~180 days.
./tx-load-test extend-ttl
```

Rent scales linearly with extension length, so extending to 180 days costs the same per covered ledger as repeated shorter extends — at the default this is a roughly twice-a-year maintenance operation.

**Cost note:** the per-account OZ balance entries dominate the bill (hundreds of XLM for a ~4.5k-account pool at 180 days) but are the one category that maintains itself — every bench transfer extends the balances it touches by ~30 days, with that rent paid inside the bench transactions. With at-least-monthly runs, `--skip-balances` is the right routine mode: it extends only the never-self-extending infra set (single-digit XLM). Include balances only ahead of a planned idle gap longer than ~30 days; if they do archive, restore re-ups them to the 120-day minimum at a comparable price, so prepaying is insurance against downtime, not a cost saving. Infra entries (instances, wasm, pair funds) are reported individually; the per-account OZ balance entries are reported as an aggregate with min/max remaining TTL. The read-only `tools/ttl-probe` utility prints the same infra-entry TTL table without requiring `FEE_PAYER`.

### `bench`

Runs a load-test workload against an already-initialized ledger.

`bench` requires `FEE_PAYER` to be set. The supplied seed is not written to disk; it is hashed and checked against `fee_payer_hash` in `state.json` before participant accounts are re-derived from `account_indices`.
Before the benchmark starts, the tool queries the chosen RPC endpoint (either `--rpc-url` or the stored `rpc_url`) and verifies that it reports the same `network_passphrase` recorded in the state file.

| Flag | Default | Description |
|---|---|---|
| `--mode` | `sac-transfer` | Workload: `sac-transfer`, `oz-transfer`, or `soroswap` |
| `--target-rps` | `50` | Steady-state requests per second |
| `--classic-rps` | `100` | Steady-state simple-payment transactions per second; `0` disables the companion stream; each transaction carries one payment op |
| `--duration` | `100s` | Total benchmark duration |
| `--ramp-up` | `20s` | Linear ramp from 1 RPS to target |
| `--rpc-url` | *(from state file)* | Override the RPC URL stored in `state.json` |
| `--state-file` | `state.json` | Input state file path |
| `--skip-account-preflight` | `false` | Skip the sampled on-chain participant-account existence check before the benchmark starts |
| `--account-preflight-sample` | `10` | Number of participant accounts to sample during runtime preflight |
| `--trace-file` | *(disabled)* | Optional NDJSON file that captures every benchmark submit and poll request/response |
| `--metrics-file` | `metrics/tx-load-test-metrics-<timestamp>-<mode>.ndjson` | Optional flattened benchmark metrics file path |
| `--metrics-gcs-url` | *(disabled)* | Optional `gs://bucket/prefix/` destination; the metrics file is uploaded there after the run. A trailing slash appends the local filename (unique per run); auth uses Application Default Credentials |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Mode guide:**

| Mode | What it exercises | Ledger prerequisites | When to use it |
|---|---|---|---|
| `sac-transfer` | SAC token transfers using trustlined holder accounts | Benchmark assets, SAC contracts, trustlines, seeded holder balances | Lowest-friction Soroban token-transfer baseline |
| `oz-transfer` | Upgradeable OpenZeppelin token `transfer` calls | OZ token contract plus participant token balances | Soroban token path with contract-managed balance storage |
| `soroswap` | Router-based exact-input swaps across benchmark pools | Soroswap factory/router, pair contracts, seeded liquidity, trustlined participants | Highest-complexity DeFi-style traffic with pool state mutation |

**Validation:** before starting, bench checks:
- `--mode` is supported and `--target-rps > 0`.
- `--classic-rps >= 0`; when non-zero it directly sets the simple-payment tx/s rate because each transaction carries one payment op.
- The participant account pool is large enough for the derived Soroban and simple-payment source-account requirements for the requested rates and duration.
- For `sac-transfer`, the persisted state still contains the required SAC holder accounts and trustlines.

**Available modes:**
- **`sac-transfer`** -- SAC token transfers between random SAC-active participant accounts via `InvokeHostFunction`.
- **`oz-transfer`** -- transfers on the upgradeable OpenZeppelin benchmark token contract.
- **`soroswap`** -- router-based exact-input swaps across the benchmark BLTA/BLTB and BLTB/BLTC pools.

When `--classic-rps > 0`, bench also runs a parallel simple-payment companion stream that submits native XLM payments with 1 payment operation per transaction. `--classic-rps` is interpreted as transactions/sec.

**Console benchmark output:** after the attack and poll drain, bench logs:
- Submission counters: submitted, queued, httpErr, tryAgainLater, submitErrors, ambiguous
- Submit-time failure summaries: per-result-code breakdown, op-result breakdown, and normalized diagnostic summaries when available
- On-chain outcomes: included, failed, pollErr
- On-chain failure summaries: per-result-code breakdown, op-result breakdown, and normalized diagnostic summaries for repeated failures
- Vegeta metrics: request count, achieved rate (req/s), throughput (req/s), success ratio
- Latency percentiles: mean, p50, p95, p99, max
- Bytes in/out: total and mean
- HTTP status code distribution

When `--trace-file` is enabled, bench also writes every submit and poll request/response pair to the specified NDJSON file for post-run analysis.

**Metrics file output:** bench writes a flattened NDJSON metrics file. If `--metrics-file` is omitted, the default path is `metrics/tx-load-test-metrics-<timestamp>-<mode>.ndjson`.

The metrics file is newline-delimited JSON. Each workload produces one `summary` record that combines run parameters, workload parameters, submission counters, on-chain counters, latency stats, ledger stats, Vegeta metrics, and HTTP status-code counts. HTTP status codes are flattened into fields on the summary record, for example `vegeta_status_code_200`, not emitted as separate records.

Example summary fields:

```json
{
  "record_type": "summary",
  "run_mode": "sac-transfer",
  "run_target_rps": 60,
  "workload": "sac-transfer",
  "workload_target_rps": 60,
  "submission_submitted": 596,
  "on_chain_included": 596,
  "e2e_latency_p95_milliseconds": 8756.480462,
  "ledger_transactions_per_finality_ledger_total": 596,
  "vegeta_requests": 596,
  "vegeta_success_percent": 100,
  "vegeta_status_code_200": 596
}
```

If Vegeta reports request errors, those remain as additional `vegeta_error` records with the same run/workload identity fields plus `code` and `count`.

The metrics schema intentionally omits per-ledger count maps, progress snapshots, submit/on-chain breakdown records, and separate HTTP status-code records. Those details remain console or trace-file concerns where applicable.

### `teardown`

Merges all participant accounts back into the fee payer.

`teardown` requires `FEE_PAYER` to be set so the fee payer and participant accounts can be re-derived from the state file.
Before teardown starts, the tool queries the chosen RPC endpoint (either `--rpc-url` or the stored `rpc_url`) and verifies that it reports the same `network_passphrase` recorded in the state file.

| Flag | Default | Description |
|---|---|---|
| `--state-file` | `state.json` | State file to consume |
| `--rpc-url` | *(from state file)* | Override the RPC URL stored in `state.json` |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

Accounts are merged in batches of 12 using two sequential fee-bumped transactions per batch:
1. **Drain** -- pay all non-zero trustline balances back to the fee payer.
2. **Merge** -- remove trustlines (ChangeTrust limit=0) then AccountMerge.

On full success, deletes the state file. On partial failure, updates the state file with remaining accounts so a subsequent `teardown` can resume.

### `sync`

Reconciles the state file with on-chain account existence.

`sync` also requires `FEE_PAYER` to be set so participant accounts can be re-derived from the stored indices.
Before sync starts, the tool queries the chosen RPC endpoint (either `--rpc-url` or the stored `rpc_url`) and verifies that it reports the same `network_passphrase` recorded in the state file.

```bash
./tx-load-test sync --state-file state.json
```

Removes entries for accounts that no longer exist on-chain, useful after a network reset or manual cleanup.

## State File

`state.json` is a flat JSON file containing everything needed to run benchmarks and teardown without re-running setup:

```json
{
  "rpc_url": "https://soroban-testnet.stellar.org",
  "network_passphrase": "Test SDF Network ; September 2015",
  "fee_payer_hash": "5d7a...hex-sha256...",
  "account_ranges": ["1-4500"],
  "sac_holder_ranges": ["1-900"],
  "assets": ["BLTA", "BLTB", "BLTC"],
  "sacs": ["C...", "C...", "C..."],
  "soroswap_factory_contract": "C...",
  "soroswap_router_contract": "C...",
  "soroswap_pair_contracts": ["C...", "C..."],
  "oz_token_contract": "C...",
  "cleaned_up": false
}
```

Participant accounts are recorded as derivation-index **ranges**: each entry is a single index (`"5"`) or an inclusive contiguous run (`"1-4500"`), sorted ascending and non-overlapping. A freshly set-up pool is a single range; `sync` pruning or a partially failed teardown batch simply produce more ranges (`["1-3999", "4100-4500"]`). This keeps the file a few hundred bytes regardless of pool size — small enough to inline in a Kubernetes ConfigMap. Legacy files using flat `account_indices` / `sac_holder_indices` arrays are still read transparently and are upgraded to the range form on the next save.

The file is written atomically (write to `.tmp`, then rename) to avoid corruption from interrupted writes. No raw seeds are stored on disk. The recorded `rpc_url` and `network_passphrase` are treated as part of the state identity and are validated before the state is reused.
