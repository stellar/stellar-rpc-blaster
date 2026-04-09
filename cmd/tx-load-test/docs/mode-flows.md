# Mode Flows

Setup is one shared superset flow, executed in this order in `cmd/tx-load-test/setup/setup.go`:

1. fee payer
2. benchmark assets
3. accounts
4. SAC deployment
5. Soroswap core
6. Soroswap pools
7. Soroswap liquidity
8. OZ token deployment and minting

## Setup Flow By Mode

### `sac-transfer`

- Shared setup creates the fee payer and benchmark assets `BLTA`, `BLTB`, `BLTC`.
- Accounts are provisioned, with a trustlined holder subset sized for SAC-style activity.
- SAC instances are deployed for all three classic assets in `cmd/tx-load-test/setup/sac.go`.
- No OZ- or Soroswap-specific state is required by the SAC mode itself, but shared setup still provisions them so one setup can serve all modes.

### `oz-transfer`

- Shared setup does everything above.
- OZ token Wasm is loaded, the OZ contract is deployed deterministically if missing, and participant balances are reconciled and minted in `cmd/tx-load-test/setup/oz_token.go`.
- Result: every participant account should have a positive OZ balance.

### `soroswap`

- Shared setup does everything above.
- Soroswap factory and router are resolved or bootstrapped.
- Exactly two benchmark pools are created or reused from `BenchmarkPairs` in `cmd/tx-load-test/soroswap/support.go` and `cmd/tx-load-test/setup/soroswap.go`:
1. BLTA/BLTB
2. BLTB/BLTC
- Each pool is seeded with liquidity if empty in `cmd/tx-load-test/setup/soroswap.go`.
- The trustlined holder subset created earlier is what makes participant accounts usable as swap traders.

## Benchmark Flow By Mode

All benchmark runs share the same outer structure in `cmd/tx-load-test/benchmark/benchmark.go`:

1. validate config and state
2. build the shared account lease manager
3. verify mode readiness
4. build the mode targeter
5. run the Vegeta attack
6. submit via `sendTransaction`
7. poll accepted hashes to terminal state
8. log submission, on-chain, and latency summaries
9. optionally run the parallel simple-payment companion stream if `classic-rps > 0`

### `sac-transfer`

- Picks a random SAC among the 3 deployed SACs.
- Picks a random trustlined holder as logical sender and a different trustlined holder as receiver.
- Uses a leased account as the tx source, with the holder account as the operation source when needed.
- Builds a SAC `transfer(src, dst, amount)` invocation and submits it fee-bumped.
- This is the only mode where tx source and logical token sender are intentionally decoupled.

### `oz-transfer`

- Picks a leased participant account as both tx source and token sender.
- Picks a different participant account as receiver from the same eligible pool.
- Builds an OZ `transfer(src, dst, amount)` invocation and submits it fee-bumped.
- This is symmetric peer-to-peer token transfer among participant accounts.

### `soroswap`

- Uses a leased trustlined account as tx source, op source, and trader.
- Builds four swap templates total from the two pools, one in each direction per pool, in `cmd/tx-load-test/benchmark/soroswap_templates.go`.
- Selects templates round-robin, so load is evenly split across both pools and both directions.
- Rewrites the trader address in the template to the leased account, rebuilds the footprint, then submits the router swap fee-bumped.
- There is no separate destination account; the trader swaps its own balances.