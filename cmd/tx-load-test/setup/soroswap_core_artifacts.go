package setup

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

var soroswapPairWasmPaths = []string{
	"contracts/soroswap/contracts/pair/target/wasm32v1-none/release/soroswap_pair.wasm",
	"contracts/soroswap/contracts/pair/target/wasm32-unknown-unknown/release/soroswap_pair.wasm",
}

var soroswapFactoryWasmPaths = []string{
	"contracts/soroswap/contracts/factory/target/wasm32v1-none/release/soroswap_factory.wasm",
	"contracts/soroswap/contracts/factory/target/wasm32-unknown-unknown/release/soroswap_factory.wasm",
}

var soroswapRouterWasmPaths = []string{
	"contracts/soroswap/contracts/router/target/wasm32v1-none/release/soroswap_router.wasm",
	"contracts/soroswap/contracts/router/target/wasm32-unknown-unknown/release/soroswap_router.wasm",
}

type soroswapCoreArtifact struct {
	label         string
	wasm          []byte
	wasmPath      string
	wasmHash      xdr.Hash
	contractID    xdr.ContractId
	contractIDStr string
	preimage      xdr.ContractIdPreimage
}

type soroswapCoreArtifacts struct {
	pair    soroswapCoreArtifact
	factory soroswapCoreArtifact
	router  soroswapCoreArtifact
}

func loadSoroswapCoreArtifacts(networkPassphrase, deployerAddress string) (soroswapCoreArtifacts, error) {
	pair, err := loadSoroswapWasmArtifact("Soroswap pair", soroswapPairWasmPaths)
	if err != nil {
		return soroswapCoreArtifacts{}, err
	}
	factory, err := loadSoroswapContractArtifact(networkPassphrase, deployerAddress, "Soroswap factory", soroswapFactoryWasmPaths, soroswapFactorySalt())
	if err != nil {
		return soroswapCoreArtifacts{}, err
	}
	router, err := loadSoroswapContractArtifact(networkPassphrase, deployerAddress, "Soroswap router", soroswapRouterWasmPaths, soroswapRouterSalt())
	if err != nil {
		return soroswapCoreArtifacts{}, err
	}
	return soroswapCoreArtifacts{pair: pair, factory: factory, router: router}, nil
}

func loadSoroswapWasmArtifact(label string, paths []string) (soroswapCoreArtifact, error) {
	wasm, wasmPath, err := readSoroswapWasm(label, paths)
	if err != nil {
		return soroswapCoreArtifact{}, err
	}
	return soroswapCoreArtifact{
		label:    label,
		wasm:     wasm,
		wasmPath: wasmPath,
		wasmHash: xdr.Hash(sha256.Sum256(wasm)),
	}, nil
}

func loadSoroswapContractArtifact(
	networkPassphrase string,
	deployerAddress string,
	label string,
	paths []string,
	salt xdr.Uint256,
) (soroswapCoreArtifact, error) {
	artifact, err := loadSoroswapWasmArtifact(label, paths)
	if err != nil {
		return soroswapCoreArtifact{}, err
	}
	artifact.contractID, artifact.contractIDStr, artifact.preimage, err = deterministicContractIdentity(networkPassphrase, deployerAddress, salt)
	if err != nil {
		return soroswapCoreArtifact{}, fmt.Errorf("derive %s contract ID: %w", label, err)
	}
	return artifact, nil
}

func readSoroswapWasm(label string, paths []string) ([]byte, string, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, path, nil
		}
		if !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("read %s Wasm %q: %w", label, path, err)
		}
	}
	return nil, "", fmt.Errorf("%s Wasm not found; build it first and place it at one of: %s", label, strings.Join(paths, ", "))
}

func deterministicContractIdentity(
	networkPassphrase string,
	deployerAddress string,
	salt xdr.Uint256,
) (xdr.ContractId, string, xdr.ContractIdPreimage, error) {
	accountID, err := xdr.AddressToAccountId(deployerAddress)
	if err != nil {
		return xdr.ContractId{}, "", xdr.ContractIdPreimage{}, fmt.Errorf("parse deployer address: %w", err)
	}

	preimage := xdr.ContractIdPreimage{
		Type: xdr.ContractIdPreimageTypeContractIdPreimageFromAddress,
		FromAddress: &xdr.ContractIdPreimageFromAddress{
			Address: xdr.ScAddress{
				Type:      xdr.ScAddressTypeScAddressTypeAccount,
				AccountId: &accountID,
			},
			Salt: salt,
		},
	}

	networkID := xdr.Hash(sha256.Sum256([]byte(networkPassphrase)))
	hashPreimage := xdr.HashIdPreimage{
		Type: xdr.EnvelopeTypeEnvelopeTypeContractId,
		ContractId: &xdr.HashIdPreimageContractId{
			NetworkId:          networkID,
			ContractIdPreimage: preimage,
		},
	}
	preimageBytes, err := hashPreimage.MarshalBinary()
	if err != nil {
		return xdr.ContractId{}, "", xdr.ContractIdPreimage{}, fmt.Errorf("marshal contract ID preimage: %w", err)
	}

	contractID := xdr.ContractId(sha256.Sum256(preimageBytes))
	contractIDStr, err := ledger.EncodeContractID(contractID)
	if err != nil {
		return xdr.ContractId{}, "", xdr.ContractIdPreimage{}, fmt.Errorf("encode contract ID: %w", err)
	}
	return contractID, contractIDStr, preimage, nil
}

func soroswapFactorySalt() xdr.Uint256 {
	return xdr.Uint256(sha256.Sum256([]byte("stellar-rpc-blaster/tx-load-test/soroswap/factory/v1")))
}

func soroswapRouterSalt() xdr.Uint256 {
	return xdr.Uint256(sha256.Sum256([]byte("stellar-rpc-blaster/tx-load-test/soroswap/router/v1")))
}
