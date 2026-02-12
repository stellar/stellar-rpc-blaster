# stellar-rpc-blaster
CLI load testing tool for Stellar RPC

This repo is home to our RPC load testing tool. It has the ability to test various endpoints on one's RPC server. ## Run Tests

Example usage on localhost RPC testnet instance (note that instance must be running separately):
./stellar-rpc-blaster run \
  --rpc-url "http://127.0.0.1:8000" \
  --config-path "./internal/config/config.example.toml" \
  --duration 30s \
  --ramp-up 10s

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