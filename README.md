# stellar-rpc-blaster

> **Note:** This repository is currently in development and is not yet ready for use. 
> Please see branch `pseudomain` for the current developmental status of `stellar-rpc-blaster`, our upcoming CLI load testing tool for Stellar RPC.  
  
Planned future usage of Blaster is as follows for the following commands `run` and `generate`, which run a load test and generate seed data  
for endpoints requiring data-backed parameters respectively.  
  
Example `run` usage on localhost RPC testnet instance:  
```  
./stellar-rpc-blaster run \
  --rpc-url <RPC_URL> \
  --config-path <PATH_TO_CONFIG_FILE>  \
  --duration <DURATION_TIME> \
  --ramp-up <RAMP_UP_TIME>
```  
Note that the default config file is found at "./internal/config/config.example.toml"  
  
To generate data for the `generate` command, example usage is as follows:  
```  
./stellar-rpc-loadtest generate \
  --rpc-url <RPC_URL> \
  [--output <PATH_TO_REQUEST_DATA_DAT>] \
  [--ledger-window START[,END]]
  [--count NUM_LEDGERS]
```  
Note that for either command, should one wish to run Blaster on a localhost RPC `testnet` instance, they may use  
`--rpc-url "http://127.0.0.1:8000"` (note that said `testnet` instance must be running separately).  
Several defaults are present with the following behaviors:  
-- `output` defaults to `/stellar-rpc-blaster/output/seed.json`  
-- `count` defaults to 5000. This is the number of ledgers to be sampled, whereas `ledger-window` is the range  
over which we're sampling.  
-- if `ledger-window` is not present, it will default to [LatestCheckpoint - `count`, LatestCheckpoint]  
-- if `END` is not present in `ledger-window`, then `ledger-window` defaults to [`START`, LatestCheckpoint]  
-- if `len(ledger-window) > count`, then at least `count` ledgers uniformly spaced in `ledger-window` will be sampled
