package setup

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

func TestSupportsSoroswapAutoBootstrap(t *testing.T) {
	require.True(t, SupportsSoroswapAutoBootstrap(soroswapStandaloneNetworkPassphrase))
	require.True(t, SupportsSoroswapAutoBootstrap(network.FutureNetworkPassphrase))
	require.False(t, SupportsSoroswapAutoBootstrap(network.TestNetworkPassphrase))
}

func TestReadSoroswapWasmUsesFirstExistingPath(t *testing.T) {
	tempDir := t.TempDir()
	firstPath := filepath.Join(tempDir, "first.wasm")
	secondPath := filepath.Join(tempDir, "second.wasm")
	require.NoError(t, os.WriteFile(firstPath, []byte("first"), 0o600))
	require.NoError(t, os.WriteFile(secondPath, []byte("second"), 0o600))

	wasm, path, err := readSoroswapWasm("Soroswap pair", []string{firstPath, secondPath})
	require.NoError(t, err)
	require.Equal(t, firstPath, path)
	require.Equal(t, []byte("first"), wasm)
}

func TestReadSoroswapWasmSkipsMissingPaths(t *testing.T) {
	tempDir := t.TempDir()
	missingPath := filepath.Join(tempDir, "missing.wasm")
	existingPath := filepath.Join(tempDir, "existing.wasm")
	require.NoError(t, os.WriteFile(existingPath, []byte("router"), 0o600))

	wasm, path, err := readSoroswapWasm("Soroswap router", []string{missingPath, existingPath})
	require.NoError(t, err)
	require.Equal(t, existingPath, path)
	require.Equal(t, []byte("router"), wasm)
}

func TestReadSoroswapWasmReportsMissingArtifacts(t *testing.T) {
	_, _, err := readSoroswapWasm("Soroswap factory", []string{"/missing/one.wasm", "/missing/two.wasm"})
	require.EqualError(t, err, "Soroswap factory Wasm not found; run contracts/update-wasms.sh or place it at one of: /missing/one.wasm, /missing/two.wasm")
}

func TestLoadSoroswapContractArtifactDerivesIdentityAndHash(t *testing.T) {
	tempDir := t.TempDir()
	wasmPath := filepath.Join(tempDir, "factory.wasm")
	wasm := []byte("factory-wasm")
	require.NoError(t, os.WriteFile(wasmPath, wasm, 0o600))
	deployer, err := keypair.Random()
	require.NoError(t, err)

	artifact, err := loadSoroswapContractArtifact(network.FutureNetworkPassphrase, deployer.Address(), "Soroswap factory", []string{wasmPath}, soroswapFactorySalt())
	require.NoError(t, err)
	require.Equal(t, "Soroswap factory", artifact.label)
	require.Equal(t, wasmPath, artifact.wasmPath)
	require.Equal(t, xdr.Hash(sha256.Sum256(wasm)), artifact.wasmHash)
	require.NotEmpty(t, artifact.contractIDStr)
	require.Equal(t, soroswapFactorySalt(), artifact.preimage.FromAddress.Salt)

	decodedID, err := ledger.DecodeContractID(artifact.contractIDStr)
	require.NoError(t, err)
	require.Equal(t, artifact.contractID, decodedID)

	again, err := loadSoroswapContractArtifact(network.FutureNetworkPassphrase, deployer.Address(), "Soroswap factory", []string{wasmPath}, soroswapFactorySalt())
	require.NoError(t, err)
	require.Equal(t, artifact.contractID, again.contractID)
	require.Equal(t, artifact.contractIDStr, again.contractIDStr)
}
