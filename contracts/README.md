## Contract Wasm Artifacts

Runtime setup only needs these compiled Wasm files in this directory:

- `oz_token.wasm`
- `soroswap_pair.wasm`
- `soroswap_factory.wasm`
- `soroswap_router.wasm`

Refresh all four artifacts with:

```bash
./contracts/update-wasms.sh
```

The script rebuilds `oz_token.wasm` from the local OpenZeppelin contract source under `contracts/oz_token/`, clones a fresh Soroswap core repo into a temporary directory, rebuilds the pair/factory/router contracts, and copies the resulting Wasm files back into this directory.