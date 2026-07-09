# tx-load-test Recreation Plan

This document is for a future coding agent that must recreate the `cmd/tx-load-test` tool without relying on the original source code. The goal is not byte-for-byte reproduction. The goal is a clean reimplementation with the same feature set, similar package boundaries, the same user-facing workflow, and a comparable test suite.

Treat this as a design brief plus acceptance criteria.

## 1. Mission

Build a standalone Stellar Soroban RPC load-testing CLI named `tx-load-test` under `cmd/tx-load-test`.

The tool must support five commands:

1. `setup`
2. `restore`
3. `bench`
4. `teardown`
5. `sync`

The tool must implement a state-file-driven lifecycle. `restore` is an
optional pre-benchmark maintenance step that re-hydrates archived Soroban
state; `bench` is repeatable; cleanup and reconciliation are explicit.

```text
setup  -->  state.json  -->  restore  (optional, pre-benchmark maintenance)
											 -->  bench     (repeatable)
											 -->  teardown
											 -->  sync
```

The design intent is:

- expensive ledger initialization happens once
- benchmark runs are repeatable without re-running setup
- cleanup is explicit and resumable
- state survives interruptions and can be reconciled against chain reality

## 2. Non-Negotiable Product Behavior

The reimplementation must preserve these behaviors.

### 2.1 CLI shape

- Use `cobra` for the command tree.
- Provide a root command `tx-load-test` with subcommands `setup`, `restore`, `bench`, `teardown`, and `sync`.
- Root command should print usage when invoked without a subcommand.
- Build target must remain:

```bash
go build -o tx-load-test ./cmd/tx-load-test
```

### 2.2 State-driven operation

- `setup` writes a JSON state file, default `state.json`.
- `bench`, `teardown`, and `sync` operate from that state file.
- The state file must be written atomically: write temp file then rename.
- No secret seeds are stored on disk.
- Instead, store a SHA-256 hash of the fee payer seed and re-derive accounts from indices.

### 2.3 Network identity validation

- Before reusing state, query the RPC server and verify the reported network passphrase matches the passphrase stored in the state file.
- `rpc_url` may be overridden later, but only if the endpoint reports the same passphrase.

### 2.4 One setup serves all benchmark modes

This is a major architectural decision and must be preserved.

- `setup` must provision the superset of ledger state needed to run all three Soroban benchmark modes:
	- `sac-transfer`
	- `oz-transfer`
	- `soroswap`
- Users must not need to re-run setup when switching between those modes.
- Setup sizing must validate the requested `--accounts`, `--target-rps`, `--classic-rps`, and `--duration` against the all-mode superset, not just one selected benchmark.

### 2.5 Parallel classic companion stream

- `bench` must optionally run a companion simple-payment stream in parallel with the Soroban benchmark stream.
- `--classic-rps` is measured in transactions per second for the companion simple-payment stream.
- Simple payments submit exactly 1 payment operation per transaction.

### 2.6 Contract artifact policy

Preserve the current artifact layout policy:

- runtime should only need flat Wasm files under `contracts/`
- required artifacts:
	- `contracts/oz_token.wasm`
	- `contracts/soroswap_pair.wasm`
	- `contracts/soroswap_factory.wasm`
	- `contracts/soroswap_router.wasm`
- `setup` on standalone/futurenet may auto-upload and deploy Soroswap core from those flat artifacts
- add and preserve a refresh script at `contracts/update-wasms.sh`
	- rebuild local OZ contract Wasm from `contracts/oz_token/`
	- clone a fresh Soroswap core repo to a temp directory
	- build pair/factory/router Wasms there
	- copy resulting Wasms into `contracts/`

### 2.7 Resumability and repair

- `setup` should tolerate partial progress and write partial state if interrupted.
- `teardown` should tolerate partial progress and update state with remaining accounts.
- `sync` should reconcile the state file with on-chain reality by dropping accounts that no longer exist.

## 3. User-Facing Features

### 3.1 `setup`

Implement these inputs and behaviors.

Flags:

- `--rpc-url` required
- `--network` required; support `testnet`, `futurenet`, `mainnet`, `standalone`
- `--duration` default `100s`
- `--target-rps` default `50`
- `--classic-rps` default `100`
- `--soroswap-factory` optional on standalone/futurenet, required on public networks
- `--soroswap-router` optional on standalone/futurenet, required on public networks
- `--liquidity-per-pool` default `1000000`
- `--accounts` default `5000`
- `--base-reserve-xlm` default `3.0`
- `--state-file` default `state.json`
- `--log-level` default `info`

Fee payer behavior:

- read fee payer seed from `FEE_PAYER`
- if absent, generate a temporary key and fund with friendbot on testnet/futurenet only
- on re-runs against existing state, require the supplied seed and validate its hash against `fee_payer_hash`

Required setup phases, in this exact conceptual order:

1. Resolve/initialize fee payer
2. Register benchmark classic assets `BLTA`, `BLTB`, `BLTC`
3. Deterministically derive participant accounts from the fee payer seed
4. Incrementally create missing accounts if setup is re-run with a higher `--accounts`
5. Provision a formula-derived trustlined holder superset sufficient for all benchmark modes
6. Create the remaining participant accounts as XLM-only accounts
7. Deploy SAC contracts for the three benchmark assets
8. Resolve or auto-bootstrap Soroswap core
9. Create or reuse benchmark Soroswap pairs
10. Seed benchmark Soroswap liquidity if pools are empty
11. Upload/deploy the OZ benchmark token and mint balances to participant accounts
12. Persist state atomically

Operational details worth preserving:

- trustlined account creation should be batched conservatively around signature limits
- minting/funding should be batched conservatively around ledger-entry write limits
- Soroswap core auto-bootstrap should only be allowed on standalone and futurenet
- deterministic Soroswap factory/router contract IDs should be derived from stable salts plus fee payer address and network passphrase

### 3.2 `restore`

`restore` is an optional pre-benchmark maintenance command. State files that
sit idle between tests accumulate archived Soroban state (especially
`oz-transfer` per-account balances and `soroswap`/account contract data),
which makes the first benchmark hits fail or pay extra. `restore` probes the
benchmark-shaped invocations and re-hydrates what it can before a real run.

Flags:

- `--mode`, default `all`; one of `all`, `sac-transfer`, `oz-transfer`, `soroswap`
- `--dry-run`, default `false`; simulate and report what would need restore, submit nothing
- `--verify`, default `false`; after restoring, re-run the probes and fail if any still require restore
- `--account-start`, default `0`; 0-based offset into the selected participant list
- `--account-limit`, default `0`; max selected accounts per applicable mode (`0` = all)
- `--progress-interval`, default `100`; log progress every N probes (negative disables)
- `--rpc-url`, optional override from state
- `--skip-account-preflight` / `--account-preflight-sample` (default `10`), same semantics as `bench`
- `--state-file`, default `state.json`
- `--log-level`, default `info`

Behavior to preserve:

- requires `FEE_PAYER`; validates network passphrase like the other state-consuming commands
- uses **simulation only** to probe; it does not submit the benchmark transfer/swap invocations
- per-mode probe scope:
	- `sac-transfer`: probes the three shared SAC contract instances (participant balances are classic trustlines, not archivable Soroban state)
	- `oz-transfer`: one probe per selected account so each account's OZ `Balance` contract-data entry is touched
	- `soroswap`: probes selected holders across both benchmark pools in both swap directions (covers shared router/pair/pool state plus per-account trader data)
- **legacy restoration**: when simulation returns a `RestorePreamble`, submit a `RestoreFootprint` transaction (unless `--dry-run`) and retry, bounded by a small attempt cap
- **autorestore awareness** (protocol 23+): when simulation omits a `RestorePreamble` but reports archived read-write entries via `SorobanResourcesExtV0.archivedSorobanEntries`, surface those as `autoRestoreProbes`/`autoRestoreKeys` in the summary but submit no separate transaction — these entries are restored inline by the benchmark's own invocations at apply time (see §7.4). `--verify` must therefore exclude autorestore-only probes when deciding whether restoration is still outstanding, or it will never converge.
- emit a per-mode summary: probes run, restore-needed probes, restore transactions submitted, autorestore probes/keys, no-op probes, restored read-only/read-write key counts, selected account range/count, elapsed time, and the first few errors. `--dry-run` summaries use "would restore" wording.

### 3.3 `bench`

Flags:

- `--mode`, default `sac-transfer`
- `--target-rps`, default `50`
- `--classic-rps`, default `100`
- `--duration`, default `100s`
- `--ramp-up`, default `20s`
- `--rpc-url`, optional override from state
- `--trace-file`, optional NDJSON submit/poll trace output
- `--metrics-file`, optional; defaults to a timestamped, mode-named NDJSON file under `metrics/`
- `--skip-account-preflight`, optional; disables the sampled on-chain participant existence preflight
- `--account-preflight-sample`, default `10`
- `--state-file`, default `state.json`
- `--log-level`, default `info`

Modes:

1. `sac-transfer`
2. `oz-transfer`
3. `soroswap`

Mode semantics:

- `sac-transfer`: random SAC transfers among SAC-enabled participant accounts
- `oz-transfer`: OZ benchmark token transfers among participant accounts
- `soroswap`: router-driven exact-input swaps across benchmark pools

Benchmark engine expectations:

- use Vegeta-style attack generation with ramp-up pacing
- submit traffic through the RPC endpoint using the RPC `sendTransaction` method
- wrap benchmark submissions in fee-bump envelopes paid by the fee payer while preserving the inner workload transaction semantics
- sample a per-op inclusion fee per transaction rather than using one fixed value (see §7.2) and raise that floor for resource-heavy transactions
- thread the simulator's autorestore extension through to each submitted transaction (see §7.4)
- maintain a separate poll/drain stage to observe inclusion results, scheduled so polling never blocks per transaction (see §7.2)
- report submission counters, acceptance failures, ambiguous submission outcomes, on-chain inclusion/failure counts, Vegeta metrics, latency percentiles, byte counts, and HTTP code distribution
- preserve submit-time and on-chain failure summaries, including result-code breakdowns, operation-result breakdowns, and normalized diagnostic summaries when available
- write a flattened NDJSON metrics file as a first-class output (see §7.5)

Validation rules:

- benchmark mode must be known
- `--target-rps > 0`
- `--classic-rps >= 0`
- if `--classic-rps > 0`, it directly determines the simple-payment tx/s rate
- bench should perform a small sampled on-chain participant-account existence preflight by default, and allow it to be disabled explicitly
- account pool must be large enough for both the Soroban stream and the classic companion stream for the requested duration and rate
- `sac-transfer` requires valid SAC holder subsets and trustlines in state

### 3.4 `teardown`

Behavior:

- requires `FEE_PAYER`
- re-derives all participant accounts from stored indices
- merges participant accounts back into the fee payer
- should operate in resumable batches
- on full success, delete the state file
- on partial failure, update the state file with remaining accounts

Teardown mechanics to preserve:

- two-step fee-bumped batch flow
	- drain balances first
	- remove trustlines then account-merge
- batching is conservative to avoid signature and operation limits

### 3.5 `sync`

Behavior:

- requires `FEE_PAYER`
- re-derives participant accounts from stored indices
- checks which on-chain accounts still exist
- removes missing accounts from the state file

## 4. Expected State Schema

The state model should be simple and flat. Preserve these fields or their equivalent meanings:

```json
{
	"rpc_url": "https://...",
	"network_passphrase": "...",
	"fee_payer_hash": "hex-sha256",
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

State design requirements:

- participant account derivation indices are the canonical identity for the
	pool; on disk they are stored as compact ranges (`account_ranges`), where
	each entry is a single index (`"5"`) or an inclusive contiguous run
	(`"1-4500"`), sorted ascending and non-overlapping. Ranges are a SET
	encoding, not a contiguity assumption: gaps from `sync` pruning or partially
	failed teardown batches simply produce more ranges. This keeps the file a
	few hundred bytes regardless of pool size (thousands of accounts would
	otherwise mean thousands of array elements), which matters for embedding
	the file in orchestration config (e.g. a Kubernetes ConfigMap).
- `sac_holder_ranges` track the subset that must hold trustlines and token
	balances for SAC-style activity, in the same encoding
- readers must also accept the legacy flat forms (`account_indices`,
	`sac_holder_indices`) and upgrade to ranges on the next save; a file
	carrying both encodings for the same field is rejected as ambiguous
- the range codec must guard expansion size on load (a typo'd `"1-4000000000"`
	must error, not allocate gigabytes)
- fee payer secret must never appear in the file
- the recorded network passphrase is part of state identity, not just metadata

## 5. Package Architecture To Recreate

Keep the codebase orderly. Recreate similar package seams.

### 5.1 Top-level CLI package

Under `cmd/tx-load-test/`:

- `main.go`: execute root command
- `root_cmd.go`: build Cobra root command
- `setup_cmd.go`
- `restore_cmd.go`
- `bench_cmd.go`
- `teardown_cmd.go`
- `sync_cmd.go`
- `cmd_common.go`: shared flag/config/state helpers

### 5.2 `config`

Responsibilities:

- `Config` struct for all command parameters
- benchmark mode enum-like type
- defaults
- network name to passphrase mapping helpers if needed

### 5.3 `setup`

This package is the most orchestration-heavy package. Preserve its decomposition.

Recommended structure:

- a `Step` abstraction or similar for ordered setup steps
- fee payer step
- assets step
- accounts step
- SAC deployment step
- Soroswap core bootstrap step
- Soroswap pairs/liquidity step
- OZ token deployment/mint step

Important internal split to preserve:

- `soroswap_core.go`: high-level orchestration only
- `soroswap_core_artifacts.go`: Wasm loading, hashing, deterministic identity derivation
- `soroswap_core_actions.go`: Wasm upload, contract deployment, factory/router initialization

Also preserve the account-oriented split that emerged from refactoring:

- account planning math
- account provisioning/execution
- account reconciliation logic for re-runs

### 5.4 `benchmark`

Responsibilities:

- benchmark config validation
- source-account sizing math for the Soroban stream and the optional classic companion stream
- shared source-account lease management for benchmark traffic, including unique request IDs, sequence assignment, and release semantics for retryable, consumed, and ambiguous outcomes
- a parallel pool of recovery workers that re-load on-chain sequence state for poisoned/ambiguous accounts before reuse (see §7.1)
- benchmark runners and attack orchestration
- a poll scheduler that batches `getTransaction`, requeues non-terminal results, and never blocks per transaction (see §7.2)
- mode-specific payload generation, including per-mode footprint rewrite and autorestore-index remapping (see §7.4)
- shared transaction building, including the autorestore extension and per-tx fee sampling
- flattened NDJSON metrics reporting (see §7.5)
- the `restore` maintenance logic (probing + legacy restore submission + autorestore accounting)

Important design choices to preserve:

- one shared Soroban transaction builder for all Soroban modes rather than ad hoc request construction in each mode
- `restore` lives alongside the benchmark code because it reuses the same mode payloads and simulation helpers as a probe; it differs only in submitting `RestoreFootprint` (or nothing) instead of the workload invocation

### 5.5 `soroban`

Use this as the shared low-level Soroban helper package.

Responsibilities:

- simulation helpers, including capturing the simulator's `SorobanTransactionData.Ext` (autorestore extension) alongside resources and footprint
- footprint manipulation and substitution
- SCVal/address/encoding helpers

Important design choice:

- simulation and footprint substitution belong in shared helpers, not duplicated in each benchmark mode
- the simulation result must carry the autorestore extension, not just resources/footprint, or every consumer silently drops it

### 5.6 `soroswap`

Use this package for Soroswap-specific read-only helpers and contract support logic.

Responsibilities:

- read-only contract calls
- router/factory support helpers
- any Soroswap-specific rewrite or convenience behavior needed by setup/bench

### 5.7 `state`

Responsibilities:

- state file load/save
- account derivation from fee payer seed + indices
- shared submission helpers
- result polling and transaction outcome decoding
- benchmark account-count math helpers used by validation, including the source-account reuse-gap and run-budget sizing constants

### 5.8 `ledger`

Responsibilities:

- low-level ledger existence checks
- contract ID encode/decode helpers
- account/contract presence inspection used by setup and sync

### 5.9 `syncstate`

Responsibilities:

- reconcile persisted state with on-chain account existence

### 5.10 `teardown`

Responsibilities:

- balance drain batching
- trustline removal + account merge batching
- resumable cleanup state updates

## 6. Cross-Cutting Design Rules

These rules matter because they reflect the refactored shape, not just the feature list.

1. Prefer small orchestration files plus focused helper files over giant mixed-responsibility files.
2. Put shared Soroban behavior in shared helpers once, not separately in each mode.
3. Keep benchmark modes thin by pushing transaction building into shared builders.
4. Keep setup idempotent wherever practical.
5. Design all state-changing phases so a partial failure can be retried safely.
6. Validate early and produce specific error messages for missing contracts, undersized account pools, invalid rates, and network mismatches.
7. Favor deterministic derivation and reproducibility over ad hoc randomness in setup.
8. Use flat runtime Wasm artifacts and a script-driven refresh workflow instead of checking in an entire vendored Soroswap source tree.

## 7. Benchmark Workload Details

### 7.1 Account pool sizing and lease management

This is essential. Recreate both the sizing math and the runtime account-coordination model, and test them.

- derive required Soroban source-account counts from requested rate and duration
- derive required classic simple-payment source-account counts from requested classic rate and duration
- validate total accounts against the combined requirement when both streams are enabled
- preserve the idea that setup validates against the all-mode superset and bench validates against the chosen mode plus optional classic stream
- use a shared source-account lease manager at runtime rather than static per-workload partitions
- preserve capability-aware leasing so workloads that need trustlined-capable sources can require them while other workloads can draw from the broader pool
- preserve explicit lease release semantics for retryable, consumed, and ambiguous outcomes so local sequence state is not blindly rewound
- preserve background recovery of ambiguous or poisoned accounts by reloading on-chain sequence state before reuse. Recovery MUST be parallel (a pool of recovery workers), not a single serialized loop: under load, a single worker blocked on one slow sequence reload lets poisoned accounts accumulate faster than they drain, collapsing the available pool and stalling the whole benchmark.
- size the pool from a source-account reuse gap (a small number of ledgers a given account should rest between submissions) and a per-run reuse budget, so the pool is large enough to avoid sequence contention at the requested rate and duration
- load initial on-chain sequence numbers in batches via `getLedgerEntries` (many keys per request, bounded concurrency) rather than one account lookup at a time, so cold start and recovery scale to thousands of accounts

### 7.2 Transaction submission and polling

- submit benchmark traffic through RPC using `sendTransaction`
- wrap benchmark submissions in fee-bump envelopes paid by the fee payer
- give benchmark transactions short time bounds and keep the poll timeout slightly longer than transaction expiry so late / evicted traffic is more likely to resolve as terminal failure than poll ambiguity
- keep submission and result-polling concerns separate
- capture both client-side submission outcomes and eventual on-chain outcomes
- retain breakdowns for retryable RPC errors versus transaction-level failures
- retain distinct visibility into ambiguous submission outcomes versus confirmed retryable or terminal failures
- surface result-code, op-result, and diagnostic summaries at both submit time and final on-chain outcome time

Fee handling (correctness, not tuning):

- sample a per-op inclusion fee per transaction from a range rather than using one fixed value. Stellar core's surge-pricing comparison requires a strictly greater per-op fee to displace a queued transaction; identical bids tie and all but the first are rejected `txINSUFFICIENT_FEE`. Per-tx sampling makes ties statistically negligible.
- raise that inclusion-fee floor for resource-heavy transactions (e.g. autorestore-inflated resource fees), so the fee-bump's total fee clears the floor instead of underbidding it.

Polling model (preserve the shape, not the constants):

- do not block a worker per outstanding transaction. Use a scheduler (e.g. a time-ordered queue) that holds pending hashes and dispatches due polls, so a backlog never starves new work.
- batch `getTransaction` lookups and requeue non-terminal results with backoff rather than busy-waiting; only declare a per-transaction timeout when an overall deadline is exceeded.
- defer the first poll for a submitted transaction until the next ledger after submission can plausibly have closed and been indexed; polling immediately on submit wastes a guaranteed not-found round trip. The submission response's latest-ledger close time is enough to predict this.
- a poll timeout leaves the on-chain outcome genuinely ambiguous (the tx may have been included or evicted); release such accounts as ambiguous and route them through recovery rather than optimistically rewinding or advancing the sequence.

### 7.3 Soroswap mode behavior

- benchmark pools should exist for BLTA/BLTB and BLTB/BLTC style paths
- swaps should use the router contract
- setup should ensure the router points at the chosen factory
- setup should ensure factory/router are initialized correctly when auto-bootstrapped

### 7.4 Soroban state archival and autorestore

This is a correctness requirement, not an optimization. On protocol 23+,
contract-data entries that go unused archive over time. Idle state files hit
this constantly: an `oz-transfer` account's `Balance` entry, a `soroswap`
pool/trader entry, or a contract instance can all be archived by the time a
benchmark runs. Getting this wrong manifests as `invokeHostFunctionEntryArchived`
at apply time, even though simulation succeeded.

Preserve the full chain:

- **Capture**: when simulating a benchmark invocation, the simulator may return
	no legacy `RestorePreamble` but instead populate
	`SorobanTransactionData.Ext` (`SorobanResourcesExtV0.archivedSorobanEntries`)
	with the read-write footprint indices it auto-restored. The simulation
	result must retain this extension, not just resources and footprint.
- **Reattach**: the shared transaction builder must set this extension on the
	submitted `SorobanTransactionData`. Core auto-restores the listed entries
	inline at apply time and prices it into the resource fee. Dropping the
	extension makes apply reject the archived reads.
- **Remap across footprint rewrites**: each mode reuses a presimulated template
	footprint and substitutes per-request keys. The archived indices reference
	template positions, so they must be remapped to the rewritten footprint's
	positions. Two distinct cases:
	- entries that are *kept* in place (e.g. a shared contract instance) keep
		their archived marker, remapped to the new index;
	- entries that are *substituted* must have their marker INHERITED by the
		replacement. This is the subtle one: SAC and soroswap substitute classic
		trustlines (which never archive), so they only ever carry markers on kept
		entries; but `oz-transfer` substitutes the per-account balance entries,
		which ARE the archivable ones. If the substituted entry's template
		counterpart was archived, the appended actual entry must be marked too.
	- the resulting index list must stay sorted and unique (core requires it).
- **`restore` accounting**: probes whose only restoration need is satisfied by
	autorestore submit no separate transaction; they are reported but rely on the
	benchmark's own invocations to carry the extension (see §3.2).

The template-reuse approach assumes participant entries share archival state
(true when balances/state are provisioned together at setup with identical
TTLs). Per-request simulation would be exact in all cases but defeats the
template optimization; the inheritance heuristic is the deliberate tradeoff.

### 7.5 Benchmark metrics output

`bench` must write a flattened NDJSON metrics file as a first-class deliverable
(default path under `metrics/`, timestamped and mode-named; overridable). It is
separate from the optional `--trace-file` request/response capture.

- one summary record per workload (the Soroban stream and, if enabled, the
	classic companion stream), combining run/workload parameters, submission
	counters, on-chain inclusion/failure counters, latency percentiles, Vegeta
	metrics, and flattened HTTP status-code counts
- ledger-level metrics are the diagnostically important additions: end-to-end
	latency (submit start to terminal poll), ledger finality distance (submit
	latest ledger to inclusion ledger), transactions per finality ledger, and a
	timeout-distance metric. The gap between finality distance (small) and
	end-to-end latency (large) is the primary signal that the observation
	pipeline, not the network, is the bottleneck.
- additional records for Vegeta error categories with run/workload identity
- the schema intentionally omits per-ledger count maps and per-request records;
	those remain trace-file or console concerns

## 8. Contract Behavior To Recreate

### 8.1 OZ benchmark token

- use an upgradeable OpenZeppelin-based benchmark token contract
- deploy deterministically from the fee payer identity and a stable salt
- mint participant balances in batches
- read runtime Wasm from `contracts/oz_token.wasm`

### 8.2 Soroswap core

- read pair/factory/router Wasm from flat files in `contracts/`
- derive deterministic contract IDs for factory and router from fee payer address + network passphrase + stable salts
- upload Wasm if not already on ledger
- deploy contracts if not already present
- initialize factory with fee payer as `fee_to_setter` and pair Wasm hash
- initialize router with the resolved factory contract
- verify existing deployed contracts are consistent with the expected configuration

## 9. Test Suite To Recreate

The reimplementation must include a test suite with similar scope and file boundaries. The exact test names may differ, but the coverage should resemble the current suite.

Current test inventory to emulate:

- `cmd/tx-load-test/main_test.go`
- `cmd/tx-load-test/benchmark/benchmark_test.go`
- `cmd/tx-load-test/benchmark/tx_builder_test.go`
- `cmd/tx-load-test/benchmark/footprint_test.go`
- `cmd/tx-load-test/benchmark/runner_test.go`
- `cmd/tx-load-test/benchmark/runner_polling_test.go`
- `cmd/tx-load-test/benchmark/restore_test.go`
- `cmd/tx-load-test/setup/setup_test.go`
- `cmd/tx-load-test/setup/accounts_test.go`
- `cmd/tx-load-test/setup/soroswap_core_test.go`
- `cmd/tx-load-test/soroban/simulate_test.go`
- `cmd/tx-load-test/soroban/footprint_test.go`
- `cmd/tx-load-test/soroban/helpers_test.go`
- `cmd/tx-load-test/soroswap/support_test.go`
- `cmd/tx-load-test/state/benchmark_accounts_test.go`
- `cmd/tx-load-test/state/state_test.go`
- `cmd/tx-load-test/state/tx_test.go`
- `cmd/tx-load-test/teardown/teardown_test.go`

The recreated suite should cover at least these behaviors.

### 9.1 CLI/config validation tests

- public networks require both Soroswap contracts for setup
- standalone/futurenet allow Soroswap auto-bootstrap
- partial Soroswap config is rejected
- explicit contract IDs are accepted

### 9.2 Benchmark validation tests

- valid config accepted for each mode
- unknown mode rejected
- undersized account pools rejected
- dual-stream account math validated
- `classic-rps` semantics validated for 1 payment op per transaction
- setup all-mode validation tested separately from bench-mode validation
- shared lease-manager behavior validated for retryable, consumed, and ambiguous account releases
- ambiguous-account recovery and sequence resynchronization validated

### 9.3 Shared tx-builder tests

- Soroban transaction body construction is centralized and tested
- auth entry fixtures are valid for the SDK version in use
- fee/resource-fee handling is asserted correctly
- benchmark transaction construction is fee-bumped and signed correctly
- per-tx inclusion-fee sampling, and the raised fee floor for heavy-resource transactions
- the autorestore extension is carried onto the submitted transaction body

### 9.4 Shared Soroban helper tests

- simulation helper behavior
- footprint replacement/substitution behavior
- helper SCVal/address conversions
- simulation captures the autorestore extension (`archivedSorobanEntries`) from the simulator response

### 9.4a Autorestore footprint-remap tests

- archived indices for kept entries remap to their new footprint positions
- archived indices for substituted entries are inherited by the appended replacements (the `oz-transfer` balance case), while substituted-but-never-archived entries (SAC/soroswap trustlines) carry nothing
- the resulting archived index list is sorted and unique

### 9.4b Poll-scheduler tests

- non-terminal results requeue with backoff without blocking, and newer due items are not starved
- an already-expired item still gets one final attempt before timing out
- a poll timeout releases the account as ambiguous (routed to recovery), not optimistically rewound
- the first poll is deferred toward the predicted next-close/index window

### 9.4c Restore tests

- per-mode probe selection and account-range slicing
- legacy `RestorePreamble` probes submit a `RestoreFootprint` (and `--dry-run` submits nothing)
- autorestore-only probes are counted but submit no transaction, and `--verify` does not treat them as outstanding restoration

### 9.5 Setup/account tests

- account planning math
- incremental account growth on setup re-runs
- trustlined holder subset sizing
- idempotent setup behavior where practical

### 9.6 Soroswap core tests

- auto-bootstrap support on standalone/futurenet only
- Wasm loading prefers first existing path
- missing artifact errors reference `contracts/update-wasms.sh`
- deterministic contract identity derivation and Wasm hashing are stable

### 9.7 State tests

- atomic load/save behavior
- account derivation from indices
- benchmark account-count helpers and source-account sizing math (reuse gap and run budget)
- transaction result decoding/polling helpers

### 9.8 Teardown tests

- batch construction and resumable cleanup behavior

The recreated suite does not need a specific test count, but it should be broad enough that a reviewer can see equivalent coverage, now including the polling scheduler, autorestore remap/inheritance, and restore behaviors above.

## 10. Suggested Implementation Order

Use this order to rebuild the tool with minimum rework.

### Phase 1: Skeleton and config

1. Create Cobra root and subcommands
2. Add config package and defaults
3. Add network/passphrase resolution helpers
4. Add command-line parsing and common validation

### Phase 2: State and derivation

1. Define state schema
2. Implement atomic state load/save
3. Implement fee payer seed hashing
4. Implement deterministic participant account derivation from indices

### Phase 3: Benchmark validation math

1. Implement source-account sizing helpers
2. Implement benchmark config validation
3. Implement setup all-mode validation
4. Add tests for all of the above before any heavy on-chain logic

### Phase 4: Setup ledger primitives

1. Fee payer initialization/funding
2. Benchmark asset registration
3. Participant account creation and incremental expansion
4. SAC deployment

### Phase 5: Shared Soroban support

1. Simulation helpers (capturing the autorestore extension)
2. Footprint helpers
3. Shared tx builder (autorestore extension + per-tx fee sampling)
4. Result polling/submission helpers

### Phase 6: Soroswap support

1. Flat Wasm artifact loading
2. Deterministic factory/router identity derivation
3. Wasm upload/deploy/init helpers
4. Pair creation and liquidity seeding
5. Contract refresh script in `contracts/update-wasms.sh`

### Phase 7: OZ token support

1. OZ Wasm loading
2. Deterministic deployment
3. Batched minting and reconciliation

### Phase 8: Bench runners

1. Shared source-account lease manager, sequence coordination, and parallel recovery workers
2. Poll scheduler (batched, non-blocking, close-aligned first poll)
3. Autorestore extension threading and per-mode footprint-remap/inheritance
4. SAC transfer mode
5. OZ transfer mode
6. Soroswap swap mode
7. Parallel simple-payment companion stream
8. Vegeta metrics, ledger/finality metrics, and the flattened NDJSON metrics file

### Phase 9: Restore maintenance command

1. Per-mode probe construction reusing the bench mode payloads
2. Legacy `RestoreFootprint` submission with attempt cap
3. Autorestore accounting and `--verify` convergence
4. Account-range slicing and progress/summary reporting

### Phase 10: Cleanup and sync

1. Teardown drain/merge batching
2. Partial-progress persistence
3. Sync reconciliation

### Phase 11: Documentation and polish

1. Command docs
2. State schema docs
3. Contract artifact docs
4. Example commands

## 11. Acceptance Criteria

The reimplementation is complete when all of the following are true.

1. `go build -o tx-load-test ./cmd/tx-load-test` succeeds.
2. The CLI exposes `setup`, `restore`, `bench`, `teardown`, and `sync` via Cobra.
3. Setup creates a reusable JSON state file and bench runs from it.
4. Setup can be re-run with a higher `--accounts` target and only create the delta.
5. Standalone/futurenet setup can auto-bootstrap Soroswap core from flat Wasm artifacts.
6. Public-network setup requires explicit factory/router IDs.
7. Bench supports `sac-transfer`, `oz-transfer`, and `soroswap`.
8. Bench optionally runs the companion classic payment stream.
9. Bench against archived state succeeds: the autorestore extension is carried through so apply auto-restores archived entries inline instead of failing with `invokeHostFunctionEntryArchived`.
10. Bench writes a flattened NDJSON metrics file with ledger finality/inclusion metrics.
11. Restore probes per mode, submits `RestoreFootprint` for legacy-archived state, accounts for autorestore, and `--verify` converges.
12. Teardown is resumable and deletes the state file on full success.
13. Sync removes missing on-chain accounts from persisted state.
14. Contract artifacts are refreshed via `contracts/update-wasms.sh`.
15. The test suite covers the same conceptual surface as the current implementation.

## 12. Things To Avoid

1. Do not collapse all logic into a few giant files.
2. Do not duplicate Soroban simulation/footprint logic per benchmark mode.
3. Do not make setup mode-specific; preserve the all-mode superset design.
4. Do not store raw fee payer secrets on disk.
5. Do not depend on a vendored Soroswap repo at runtime.
6. Do not make teardown a best-effort shell script; keep it a resumable first-class command.
7. Do not drop the simulator's autorestore extension when building submitted transactions, and do not lose archived markers when rewriting footprints (especially for substituted entries) — both reintroduce `invokeHostFunctionEntryArchived` failures that pass simulation.
8. Do not block a poll worker per outstanding transaction, and do not serialize account recovery behind a single worker — both cause the available pool to collapse under sustained load.
9. Do not use one fixed inclusion fee for all benchmark transactions; identical bids tie under surge pricing and get rejected.

## 13. Final Note To The Future Agent

If tradeoffs are required, preserve these first:

1. state-file-driven lifecycle
2. all-mode setup shape
3. deterministic account derivation
4. shared Soroban helpers and shared tx builder
5. flat contract artifact model plus refresh script
6. strong validation and comparable tests
7. autorestore correctness (capture, reattach, remap/inherit) — without it, archived state silently breaks every Soroban mode
8. the non-blocking poll scheduler and parallel account recovery — they are what keep throughput steady under sustained load rather than spiraling into pool starvation

You do not need to reproduce the exact line structure of the current implementation. You do need to recreate the same operational model, package boundaries, ergonomics, and reliability properties.
