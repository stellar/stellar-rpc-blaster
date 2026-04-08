package state

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stretchr/testify/require"
)

const standaloneNetworkPassphrase = "Standalone Network ; February 2017"

type rpcSuccessEnvelope struct {
	JSONRPC string                      `json:"jsonrpc"`
	ID      any                         `json:"id"`
	Result  protocol.GetNetworkResponse `json:"result"`
}

func mustRandomKeypair(t *testing.T) *keypair.Full {
	t.Helper()
	kp, err := keypair.Random()
	require.NoError(t, err)
	return kp
}

func newGetNetworkServer(t *testing.T, passphrase string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "getNetwork", req["method"])

		resp := rpcSuccessEnvelope{
			JSONRPC: "2.0",
			ID:      req["id"],
			Result: protocol.GetNetworkResponse{
				Passphrase:      passphrase,
				ProtocolVersion: 22,
			},
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
}

func TestHashSeedStable(t *testing.T) {
	const seed = "hello"
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	require.Equal(t, want, HashSeed(seed))
}

func TestDeriveKeypairRecoverIndexRoundTrip(t *testing.T) {
	base := mustRandomKeypair(t)
	indices := []int{1, 2, 17, 99, 4096}

	for _, idx := range indices {
		derived, err := DeriveKeypair(base, idx)
		require.NoError(t, err)
		got, err := RecoverIndex(derived)
		require.NoError(t, err)
		require.Equal(t, idx, got)
	}
}

func TestRecoverIndexNilKeypair(t *testing.T) {
	_, err := RecoverIndex(nil)
	require.Error(t, err)
}

func TestPersistedStateValidate(t *testing.T) {
	base := PersistedState{
		RPCURL:            "https://rpc.example",
		NetworkPassphrase: network.TestNetworkPassphrase,
		FeePayerHash:      "abc123",
	}

	tests := []struct {
		name string
		ps   *PersistedState
		want string
	}{
		{name: "valid", ps: &base, want: ""},
		{name: "nil", ps: nil, want: "nil persisted state"},
		{name: "missing rpc", ps: &PersistedState{NetworkPassphrase: base.NetworkPassphrase, FeePayerHash: base.FeePayerHash}, want: "missing rpc_url"},
		{name: "missing passphrase", ps: &PersistedState{RPCURL: base.RPCURL, FeePayerHash: base.FeePayerHash}, want: "missing network_passphrase"},
		{name: "missing hash", ps: &PersistedState{RPCURL: base.RPCURL, NetworkPassphrase: base.NetworkPassphrase}, want: "missing fee_payer_hash"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ps.Validate()
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.want)
		})
	}
}

func TestValidateSetupConfig(t *testing.T) {
	ps := &PersistedState{
		RPCURL:            "https://rpc.example",
		NetworkPassphrase: network.TestNetworkPassphrase,
		FeePayerHash:      "abc123",
	}

	require.NoError(t, ps.ValidateSetupConfig(network.TestNetworkPassphrase))
	require.Error(t, ps.ValidateSetupConfig(standaloneNetworkPassphrase))
}

func TestToPersistedStatePreservesSparseIndicesAndMetadata(t *testing.T) {
	base := mustRandomKeypair(t)
	indices := []int{1, 2, 4, 9}
	accountKPs := make([]*keypair.Full, 0, len(indices))
	for _, idx := range indices {
		kp, err := DeriveKeypair(base, idx)
		require.NoError(t, err)
		accountKPs = append(accountKPs, kp)
	}

	st := &State{
		FeePayerKP:        base,
		NetworkPassphrase: network.TestNetworkPassphrase,
		Assets: [3]txnbuild.CreditAsset{
			{Code: "BLTA", Issuer: base.Address()},
			{Code: "BLTB", Issuer: base.Address()},
			{Code: "BLTC", Issuer: base.Address()},
		},
		AccountKPs:              accountKPs,
		SACHolderKPs:            accountKPs[:3],
		SACs:                    [3]string{"C1", "C2", "C3"},
		OZTokenContract:         "COZTOKEN",
		SoroswapFactoryContract: "CFACTORY",
		SoroswapRouterContract:  "CROUTER",
		SoroswapPairContracts:   []string{"CPAIR1", "CPAIR2"},
	}

	ps, err := st.ToPersistedState("https://rpc.example")
	require.NoError(t, err)
	require.Equal(t, "https://rpc.example", ps.RPCURL)
	require.Equal(t, st.NetworkPassphrase, ps.NetworkPassphrase)
	require.Equal(t, HashSeed(base.Seed()), ps.FeePayerHash)
	require.Equal(t, []int{1, 2, 4, 9}, ps.AccountIndices)
	require.Equal(t, []int{1, 2, 4}, ps.SACHolderIndices)
	require.Equal(t, [3]string{"BLTA", "BLTB", "BLTC"}, ps.Assets)
	require.Equal(t, st.SACs, ps.SACs)
	require.Equal(t, st.OZTokenContract, ps.OZTokenContract)
	require.Equal(t, st.SoroswapFactoryContract, ps.SoroswapFactoryContract)
	require.Equal(t, st.SoroswapRouterContract, ps.SoroswapRouterContract)
	require.Equal(t, st.SoroswapPairContracts, ps.SoroswapPairContracts)
}

func TestFromPersistedStateRejectsWrongSeed(t *testing.T) {
	base := mustRandomKeypair(t)
	wrong := mustRandomKeypair(t)
	ps := &PersistedState{
		RPCURL:            "https://rpc.example",
		NetworkPassphrase: network.TestNetworkPassphrase,
		FeePayerHash:      HashSeed(base.Seed()),
		AccountIndices:    []int{1, 2, 4},
		Assets:            [3]string{"BLTA", "BLTB", "BLTC"},
	}

	_, err := FromPersistedState(ps, wrong.Seed(), "")
	require.Error(t, err)
}

func TestFromPersistedStateUsesOverrideRPCURL(t *testing.T) {
	base := mustRandomKeypair(t)
	srv := newGetNetworkServer(t, network.TestNetworkPassphrase)
	defer srv.Close()

	ps := &PersistedState{
		RPCURL:                  "https://stored.example",
		NetworkPassphrase:       network.TestNetworkPassphrase,
		FeePayerHash:            HashSeed(base.Seed()),
		AccountIndices:          []int{1, 2},
		SACHolderIndices:        []int{1, 2},
		Assets:                  [3]string{"BLTA", "BLTB", "BLTC"},
		OZTokenContract:         "COZTOKEN",
		SoroswapFactoryContract: "CFACTORY",
		SoroswapRouterContract:  "CROUTER",
		SoroswapPairContracts:   []string{"CPAIR1", "CPAIR2"},
	}

	st, err := FromPersistedState(ps, base.Seed(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, ps.OZTokenContract, st.OZTokenContract)
	require.Equal(t, ps.SoroswapFactoryContract, st.SoroswapFactoryContract)
	require.Equal(t, ps.SoroswapRouterContract, st.SoroswapRouterContract)
	require.Equal(t, ps.SoroswapPairContracts, st.SoroswapPairContracts)
	require.Len(t, st.SACHolderKPs, 2)
	netInfo, err := st.RPCClient.GetNetwork(context.Background())
	require.NoError(t, err)
	require.Equal(t, ps.NetworkPassphrase, netInfo.Passphrase)
}

func TestFromPersistedStateDefaultsSACHolderSubset(t *testing.T) {
	base := mustRandomKeypair(t)
	ps := &PersistedState{
		RPCURL:            "https://rpc.example",
		NetworkPassphrase: network.TestNetworkPassphrase,
		FeePayerHash:      HashSeed(base.Seed()),
		AccountIndices:    []int{1, 2, 3},
		Assets:            [3]string{"BLTA", "BLTB", "BLTC"},
	}

	st, err := FromPersistedState(ps, base.Seed(), ps.RPCURL)
	require.NoError(t, err)
	require.Len(t, st.SACHolderKPs, 3)
	for i, kp := range st.SACHolderKPs {
		require.Equal(t, st.AccountKPs[i].Address(), kp.Address())
	}
}

func TestValidateRPCNetwork(t *testing.T) {
	const passphrase = network.TestNetworkPassphrase

	t.Run("uses stored rpc url when override empty", func(t *testing.T) {
		srv := newGetNetworkServer(t, passphrase)
		defer srv.Close()

		ps := &PersistedState{RPCURL: srv.URL, NetworkPassphrase: passphrase, FeePayerHash: "abc123"}
		require.NoError(t, ps.ValidateRPCNetwork(context.Background(), ""))
	})

	t.Run("uses override rpc url", func(t *testing.T) {
		srv := newGetNetworkServer(t, passphrase)
		defer srv.Close()

		ps := &PersistedState{RPCURL: "https://stored.example", NetworkPassphrase: passphrase, FeePayerHash: "abc123"}
		require.NoError(t, ps.ValidateRPCNetwork(context.Background(), srv.URL))
	})

	t.Run("rejects passphrase mismatch", func(t *testing.T) {
		srv := newGetNetworkServer(t, standaloneNetworkPassphrase)
		defer srv.Close()

		ps := &PersistedState{RPCURL: srv.URL, NetworkPassphrase: passphrase, FeePayerHash: "abc123"}
		err := ps.ValidateRPCNetwork(context.Background(), "")
		require.Error(t, err)
	})
}

func TestLoadExistingSetupStateMissingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadExistingSetupState(context.Background(), dir+"/missing.json", "", "", network.TestNetworkPassphrase)
	require.NoError(t, err)
	require.Nil(t, st)
}

func TestLoadRuntimeStateUsesPersistedRPCURLWhenOverrideEmpty(t *testing.T) {
	base := mustRandomKeypair(t)
	srv := newGetNetworkServer(t, network.TestNetworkPassphrase)
	defer srv.Close()

	ps := &PersistedState{
		RPCURL:            srv.URL,
		NetworkPassphrase: network.TestNetworkPassphrase,
		FeePayerHash:      HashSeed(base.Seed()),
		AccountIndices:    []int{1},
		Assets:            [3]string{"BLTA", "BLTB", "BLTC"},
	}
	stateFile := t.TempDir() + "/state.json"
	require.NoError(t, ps.Save(stateFile))

	loaded, err := LoadRuntimeState(context.Background(), RuntimePhaseBench, stateFile, base.Seed(), "")
	require.NoError(t, err)
	require.Equal(t, srv.URL, loaded.RPCURL)
	require.NotNil(t, loaded.Live)
}
