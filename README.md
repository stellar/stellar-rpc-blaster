# stellar-rpc-blaster
CLI load testing tool for Stellar RPC

This repo is home to our RPC load testing tool. It has the ability to test various endpoints on one's RPC server. 

### Running

Example usage on localhost RPC testnet instance (note that instance must be running separately):
```
./stellar-rpc-blaster run \
  --rpc-url "http://127.0.0.1:8000" \
  --config-path "./internal/config/config.example.toml" \
  --duration 30s \
  --ramp-up 10s
```

### Using `sendTransaction`

The `sendTransaction` endpoint needs an origin account that can fund the worker accounts used during the run.

On testnet, `stellar-rpc-blaster` can create and fund that origin account automatically through friendbot if `ORIGIN_ACCOUNT_SECRET` is unset.

On pubnet, you must provide the funding account secret through the environment:

```bash
export ORIGIN_ACCOUNT_SECRET="S..."

./stellar-rpc-blaster run \
  --rpc-url "https://your-rpc-host" \
  --config-path "./internal/config/config.example.toml" \
  --duration 30s \
  --ramp-up 10s
```

To target only `sendTransaction`, enable it in the TOML config and keep the secret out of the file:

```toml
input_data_path = "./output/seed.json"

[endpoints.sendTransaction]
rps = 10
```

Notes:
- `sendTransaction` creates and minimally funds worker accounts before the test starts, wraps worker transactions in fee bumps so the origin account pays the fees, then fee-bump-merges the workers back into the origin account during cleanup.
- Worker accounts hold only the minimum balance needed to exist and authorize the inner transactions.
- The origin account must have enough XLM to fund the worker accounts and cover setup, runtime, and cleanup fees.
- The tool logs the generated seed if it auto-creates an origin account on testnet.

To generate data for the `generate` command, example usage is as follows:
```
./stellar-rpc-loadtest generate \
  --rpc-url <RPC_URL> \
  [--output <PATH_TO_REQUEST_DATA_DAT>] \
  [--ledger-window START[,END]]
  [--count NUM_LEDGERS]
```
Several defaults are present with the following behaviors:
-- `output` defaults to `/stellar-rpc-blaster/output/seed.json`
-- `count` defaults to 5000. This is the number of ledgers to be sampled, whereas `ledger-window` is the range 
over which we're sampling.
-- if `ledger-window` is not present, it will default to [LatestCheckpoint - `count`, LatestCheckpoint]
-- if `END` is not present in `ledger-window`, then `ledger-window` defaults to [`START`, LatestCheckpoint]
-- if `len(ledger-window) > count`, then at least `count` ledgers uniformly spaced in `ledger-window` will be sampled