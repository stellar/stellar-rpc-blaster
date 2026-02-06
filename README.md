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
  --network-passphrase <NETWORK_PASSPHRASE> \
  --output <PATH_TO_REQUEST_DATA_DAT> \
  [--ledger-window <NUM_LEDGERS>]
  ```