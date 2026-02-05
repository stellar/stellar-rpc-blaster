# stellar-rpc-blaster
CLI load testing tool for Stellar RPC

This repo is home to our RPC load testing tool. It has the ability to test various endpoints on one's RPC server. ## Run Tests

Example usage on localhost RPC testnet instance:
./stellar-rpc-blaster run \
  --network-passphrase "Test SDF Network ; September 2015" \
  --rpc-url "http://127.0.0.1:8000" \
  --config-path "./internal/config/config.example.toml" \
  --duration 30s \
  --ramp-up 10s
