# stellar-rpc-blaster
CLI load-testing tools for Stellar RPC.

This repo contains two tools:

- `stellar-rpc-blaster` -- configurable RPC request load generation from sampled ledger data.
- `tx-load-test` -- standalone transaction load testing for SAC transfers, OZ token transfers, Soroswap swaps, and a parallel classic-payment stream. See [cmd/tx-load-test/README.md](cmd/tx-load-test/README.md) for setup, benchmark, teardown, and metrics-output details.

## Build

```bash
go build -o stellar-rpc-blaster .
go build -o tx-load-test ./cmd/tx-load-test/
```

### Running

Example usage on localhost RPC testnet instance (note that instance must be running separately):
```
./stellar-rpc-blaster run \
  --rpc-url "http://127.0.0.1:8000" \
  --config-path "./internal/config/config.example.toml" \
  --duration 30s \
  --ramp-up 10s
```

To generate data for the `generate` command, example usage is as follows:
```
./stellar-rpc-blaster generate \
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