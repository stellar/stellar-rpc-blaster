# tx-load-test

A standalone Stellar Soroban RPC load-testing tool. It drives sustained transaction traffic against an RPC endpoint, measuring submission latency, acceptance rates, and on-chain inclusion outcomes.

## Three-Phase Design

The tool is split into three independent phases connected by a JSON state file:

```
setup  -->  state.json  -->  bench  (repeatable)
                        -->  teardown
```

- **`setup`** -- one-time ledger initialization: creates accounts, assets, trustlines, SAC contracts. Re-running with a higher `--accounts` value adds accounts incrementally.
- **`bench`** -- drives load against the RPC endpoint using pre-built state. Run as many times as needed.
- **`teardown`** -- merges all participant accounts back into the fee payer, recovering XLM. Deletes the state file on success.
- **`sync`** -- reconciles the state file with on-chain reality (removes accounts that no longer exist).

This separation means setup's expensive on-chain work is done once, benchmarks can be iterated quickly, and cleanup is explicit.

## Quick Start

```bash
# Build
go build -o tx-load-test ./cmd/tx-load-test/

# 1. Set up ledger state (testnet, 3000 accounts)
export TX_LOAD_TEST_FEE_PAYER_SEED="S..."  # optional; omit to auto-generate via friendbot
./tx-load-test setup --rpc-url https://soroban-testnet.stellar.org --accounts 3000

# 2. Run a benchmark (requires the same fee-payer seed used for setup)
export TX_LOAD_TEST_FEE_PAYER_SEED="S..."
./tx-load-test bench --mode sac-transfer --target-rps 300 --duration 60s

# 3. Need more accounts? Just re-run setup with a higher target.
./tx-load-test setup --rpc-url https://soroban-testnet.stellar.org --accounts 5000

# 4. Clean up (also requires TX_LOAD_TEST_FEE_PAYER_SEED)
./tx-load-test teardown
```

## Commands

### `setup`

Creates all required ledger state and writes `state.json`. If a state file already exists, loads it and only creates the additional accounts needed to reach the `--accounts` target.

| Flag | Default | Description |
|---|---|---|
| `--rpc-url` | *(required)* | Stellar RPC HTTP endpoint |
| `--network` | `testnet` | Network shorthand: `testnet`, `futurenet`, `mainnet`, `standalone` |
| `--network-passphrase` | *(from --network)* | Override passphrase directly |
| `--accounts` | `5000` | Target number of participant accounts |
| `--base-reserve-xlm` | `3.0` | XLM to fund each account |
| `--state-file` | `state.json` | Output state file path |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

The fee-payer seed is read from the `TX_LOAD_TEST_FEE_PAYER_SEED` environment variable. If unset, a temporary keypair is generated and funded via friendbot (testnet/futurenet only).

If `setup` is re-run against an existing `state.json`, `TX_LOAD_TEST_FEE_PAYER_SEED` must be set and must match the hash recorded in the state file.
Re-running `setup` requires the resolved network passphrase to match the value already recorded in `state.json`. The `--rpc-url` may change, but the chosen endpoint must report that same passphrase via `getNetwork`.

**Setup steps (in order):**
1. **Fee payer** -- verify/create/fund the fee-payer account. Auto-tops-up via friendbot if balance is insufficient.
2. **Assets** -- register 3 benchmark classic assets (BLTA, BLTB, BLTC) with the fee payer as issuer.
3. **Accounts** -- derive keypairs deterministically from the fee-payer seed. If a state file was loaded, only the delta accounts are created. Accounts are created in batches of 19 (CreateAccount + 3 ChangeTrust, capped at 20 signatures), then minted in batches of 33.
4. **SAC** -- deploy a Stellar Asset Contract for each of the 3 assets (idempotent; skips if already deployed).

If setup is interrupted, a best-effort cleanup merges whatever accounts exist and writes partial state so `teardown` can finish later.

**Incremental setup example:**
```bash
# Start with 2000 accounts
./tx-load-test setup --rpc-url https://... --accounts 2000

# Later, expand to 5000 -- only accounts 2001-5000 are created
./tx-load-test setup --rpc-url https://... --accounts 5000
```

### `bench`

Runs a load-test workload against an already-initialized ledger.

`bench` requires `TX_LOAD_TEST_FEE_PAYER_SEED` to be set. The supplied seed is not written to disk; it is hashed and checked against `fee_payer_hash` in `state.json` before participant accounts are re-derived from `account_indices`.
Before the benchmark starts, the tool queries the chosen RPC endpoint (either `--rpc-url` or the stored `rpc_url`) and verifies that it reports the same `network_passphrase` recorded in the state file.

| Flag | Default | Description |
|---|---|---|
| `--mode` | `sac-transfer` | Workload: `sac-transfer` |
| `--target-rps` | `50` | Steady-state requests per second |
| `--duration` | `100s` | Total benchmark duration |
| `--ramp-up` | `20s` | Linear ramp from 1 RPS to target |
| `--rpc-url` | *(from state file)* | Override the RPC URL stored in `state.json` |
| `--state-file` | `state.json` | Input state file path |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Validation:** before starting, bench checks:
- **Hard minimum:** `accounts >= target-rps * 5` (one tx per account per ledger). Fails with an error if not met.
- **Recommended minimum:** `accounts >= target-rps * 10` (two full ledgers between account reuse). Logs a warning if not met. This margin prevents mempool evictions from cascading into sequence errors.

**Available modes:**
- **`sac-transfer`** -- SAC token transfers between random participant accounts via `InvokeHostFunction`.

**Benchmark output:** after the attack and poll drain, bench logs:
- Submission counters: submitted, queued, httpErr, tryAgainLater, submitErrors (with per-ResultCode breakdown)
- On-chain outcomes: included, failed, pollErr
- Vegeta metrics: request count, achieved rate (req/s), throughput (req/s), success ratio
- Latency percentiles: mean, p50, p95, p99, max
- Bytes in/out: total and mean
- HTTP status code distribution

### `teardown`

Merges all participant accounts back into the fee payer.

`teardown` requires `TX_LOAD_TEST_FEE_PAYER_SEED` to be set so the fee payer and participant accounts can be re-derived from the state file.
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

`sync` also requires `TX_LOAD_TEST_FEE_PAYER_SEED` to be set so participant accounts can be re-derived from the stored indices.
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
  "account_indices": [1, 2, 3],
  "assets": ["BLTA", "BLTB", "BLTC"],
  "sacs": ["C...", "C...", "C..."],
  "cleaned_up": false
}
```

The file is written atomically (write to `.tmp`, then rename) to avoid corruption from interrupted writes. No raw seeds are stored on disk. The recorded `rpc_url` and `network_passphrase` are treated as part of the state identity and are validated before the state is reused.

## Architecture

### Package Layout

```
cmd/tx-load-test/
|-- main.go            # Cobra CLI: setup, bench, teardown, sync subcommands
|-- config/            # Config struct and defaults
|-- state/             # Shared types (State, PersistedState) and transaction helpers
|   |-- state.go       #   State / PersistedState types, Save / Load / conversion
|   |-- tx.go          #   SubmitAndWait, SubmitFeeBumpAndWait, SubmitAllAndPoll, etc.
|   +-- account.go     #   AccountExists helper
|-- setup/             # Setup orchestrator and step implementations
|   |-- setup.go       #   Step interface, ordered execution, incremental support
|   |-- fee_payer.go   #   Fee payer initialization, friendbot top-up loop
|   |-- assets.go      #   Register BLTA/BLTB/BLTC
|   |-- accounts.go    #   Batch account creation, trustlines, minting (delta-aware)
|   |-- sac.go         #   SAC deployment (CreateContractV2)
|   |-- oz_token.go    #   OZ token deployment (placeholder)
|   +-- soroswap.go    #   Soroswap pool setup (placeholder)
|-- benchmark/         # Benchmark harness and workload modes
|   |-- benchmark.go   #   Mode interface, ValidateConfig, Run entry point
|   |-- runner.go      #   Vegeta attack loop, poll workers, metrics collection
|   |-- sac_transfer.go#   SAC transfer targeter with presimulation + sequence mgmt
|   |-- oz_transfer.go #   OZ transfer targeter (placeholder)
|   +-- soroswap.go    #   Soroswap targeter (placeholder)
|-- teardown/          # Teardown and best-effort cleanup
|   +-- teardown.go    #   Teardown, BestEffortCleanup, batch drain+merge
+-- syncstate/         # State file reconciliation
    +-- sync.go        #   SyncState
```

### Key Design Decisions

**Incremental setup.** If `state.json` already exists when `setup` runs, the existing state is loaded and only delta accounts are created. All steps are idempotent -- fee payer, assets, and SACs skip work if already done; accounts only creates indices beyond the existing count.

**Per-account atomic sequence counters.** During benchmarks, each source account has its own `atomic.Int64` counter initialized to the on-ledger sequence number. The targeter increments the counter atomically to get the next sequence. This replaces a global slot-based formula that assumed every transaction succeeds.

**Sequence reset on non-consuming failures.** When a transaction is rejected without consuming its sequence number (TRY_AGAIN_LATER, ERROR, or mempool eviction detected by poll timeout), the runner calls `resetSeq` to decrement the account's counter. This prevents BadSeq cascades where one failed tx permanently poisons all subsequent txs for that account.

**Scaled poll workers.** The number of getTransaction poll workers scales with target RPS as `max(20, min(targetRPS/5, 200))`. At 300 RPS this gives 60 workers instead of a fixed 20, cutting post-attack drain time by ~3x.

**Deterministic keypair derivation.** Participant keypairs are derived from the fee-payer seed by overwriting the last 4 bytes with a big-endian index. This makes setup idempotent -- re-running with the same seed produces the same accounts.

**Secret-free persisted state.** The state file stores `fee_payer_hash` plus `account_indices`, not raw Stellar seeds. Bench, teardown, and sync require `TX_LOAD_TEST_FEE_PAYER_SEED` so the fee payer and participant accounts can be re-derived in memory.

**State is network-bound.** The state file stores both `rpc_url` and `network_passphrase`. Re-running setup requires the same network passphrase, and bench / teardown / sync verify that the chosen RPC endpoint (stored or overridden) reports that same network before doing any work.

**Fee-bump retry with escalation.** Setup and teardown transactions use `SubmitFeeBumpAndWait`, which retries on `TxInsufficientFee` with exponential fee escalation up to 2x the base inclusion fee (200 -> 400 stroops/op).

**Presimulation for benchmarks.** The SAC transfer targeter presimulates one transfer per SAC contract at startup to capture the exact Soroban resource budget and ledger footprint. During the attack, only the two trustline keys in the footprint are substituted per request -- all other fields are reused from the template.

**Round-robin slot assignment.** Each benchmark request claims a monotonic slot. Source account = `slot % N`. This guarantees each account appears at most once per N consecutive requests, preventing within-ledger sequence number collisions at a given RPS.

**Account pool sizing.** The hard minimum is `RPS * 5` (one tx per account per 5s ledger). The recommended minimum is `RPS * 10` (two full ledgers between reuse), ensuring the previous tx is confirmed before the account is reused. Without this margin, mempool evictions cascade into BadSeq errors.

**Ramp-to-constant pacer.** RPS increases linearly from 1 to `target-rps` over the ramp-up window, then holds constant. This avoids overwhelming the RPC at startup.

**Vegeta metrics collection.** Every Vegeta result is fed into a `vegeta.Metrics` accumulator. After the attack, latency percentiles (p50/p95/p99/max), achieved rate, throughput, success ratio, byte counts, and HTTP status code distribution are logged.

**Two-pass teardown batches.** Each batch first drains non-zero trustline balances (Payment), confirms on-chain, then removes trustlines and merges (ChangeTrust + AccountMerge). The drain must be confirmed before removal to avoid `ChangeTrustInvalidLimit` from in-flight SAC transfers.

**Incremental teardown persistence.** After each successful teardown batch, the merged accounts are removed from in-memory state and `state.json` is rewritten immediately. This makes teardown crash-safe without needing an end-of-run reconciliation pass.

### Transaction Constants

| Constant | Value | Rationale |
|---|---|---|
| Inclusion fee | 200 stroops/op | Headroom above 100 minimum without waste |
| Time bound | 300 s | ~60 ledgers; prevents stale tx inclusion |
| Benchmark tx timeout | 60 s | Tighter window for load-test transactions |
| Poll timeout | 30 s | Per-tx deadline for getTransaction polling |
| Poll workers | max(20, RPS/5) | Scales with load; capped at 200 |
| Create+trust batch | 19 accounts | 19 + 1 fee payer = 20 sigs (XDR hard cap) |
| Mint batch | 33 accounts | 33 * 3 assets = 99 ops (under 100-op limit) |
| Merge batch | 12 accounts | 12 * 4 = 48 ops merge; 12 * 3 = 36 ops drain |

## Design Diagrams

See the [design/](design/) directory for Mermaid flow diagrams:
- [Setup flow](design/setup-flow.md)
- [Benchmark flow](design/benchmark-flow.md)
- [Teardown flow](design/teardown-flow.md)
