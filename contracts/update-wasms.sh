#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
contracts_dir="$repo_root/contracts"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

soroswap_repo_url="${SOROSWAP_REPO_URL:-https://github.com/soroswap/core.git}"
soroswap_ref="${SOROSWAP_REF:-main}"

require_command() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

build_contract() {
	local dir="$1"
	local output_name="$2"
	local source_wasm="$3"

	echo "building $output_name"
	(
		cd "$dir"
		cargo build --target wasm32v1-none --release
	)
	cp "$source_wasm" "$contracts_dir/$output_name"
}

require_command cargo
require_command git
require_command mktemp

build_contract \
	"$contracts_dir/oz_token" \
	"oz_token.wasm" \
	"$contracts_dir/oz_token/target/wasm32v1-none/release/oz_token_contract.wasm"

echo "cloning Soroswap core from $soroswap_repo_url ($soroswap_ref)"
git clone --depth 1 --branch "$soroswap_ref" "$soroswap_repo_url" "$tmpdir/soroswap"

build_contract \
	"$tmpdir/soroswap/contracts/pair" \
	"soroswap_pair.wasm" \
	"$tmpdir/soroswap/contracts/pair/target/wasm32v1-none/release/soroswap_pair.wasm"

build_contract \
	"$tmpdir/soroswap/contracts/factory" \
	"soroswap_factory.wasm" \
	"$tmpdir/soroswap/contracts/factory/target/wasm32v1-none/release/soroswap_factory.wasm"

build_contract \
	"$tmpdir/soroswap/contracts/router" \
	"soroswap_router.wasm" \
	"$tmpdir/soroswap/contracts/router/target/wasm32v1-none/release/soroswap_router.wasm"

echo "updated Wasm artifacts in $contracts_dir"