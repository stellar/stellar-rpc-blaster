# oz_token

Minimal upgradeable OpenZeppelin-based Stellar fungible token used by `tx-load-test`.

## Features

- SEP-41-compatible fungible token via `stellar-tokens`
- owner-gated minting via `stellar-access::ownable`
- in-place upgrades via `stellar-contract-utils::upgradeable`
- stable contract ID across upgrades

## Constructor

`__constructor(name: String, symbol: String, initial_owner: Address)`

This initializes metadata, sets the owner, and marks schema version `1`.

## Admin entrypoints

- `mint_tokens(to: Address, amount: i128)`
- `upgrade(new_wasm_hash: BytesN<32>, operator: Address)`
- `schema_version() -> u32`

## Build

From the repo root:

```bash
cd contracts/oz_token
stellar contract build
```

The resulting Wasm is typically written under `target/wasm32v1-none/release/`.

## Notes

- Upgrades preserve the contract ID because they replace the current contract Wasm in place.
- As with any Soroban upgrade, constructor code is not re-run after upgrade.
- Storage migrations, if ever needed, should be added as separate migration entrypoints in a later version.
