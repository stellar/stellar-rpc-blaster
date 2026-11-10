package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeIndexRanges(t *testing.T) {
	cases := []struct {
		name    string
		indices []int
		want    []string
	}{
		{"empty", nil, nil},
		{"single", []int{5}, []string{"5"}},
		{"contiguous", []int{1, 2, 3, 4, 5}, []string{"1-5"}},
		{"hole in middle", []int{1, 2, 3, 5, 6}, []string{"1-3", "5-6"}},
		{"isolated singletons", []int{1, 3, 7}, []string{"1", "3", "7"}},
		{"mixed", []int{1, 2, 3, 4, 4000, 4001, 9999}, []string{"1-4", "4000-4001", "9999"}},
		{"unsorted with duplicates canonicalizes", []int{5, 1, 3, 2, 5, 4}, []string{"1-5"}},
		{"zero is a valid index", []int{0, 1, 2}, []string{"0-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, encodeIndexRanges(tc.indices))
		})
	}
}

func TestDecodeIndexRanges(t *testing.T) {
	t.Run("roundtrips", func(t *testing.T) {
		for _, indices := range [][]int{
			{1},
			{1, 2, 3, 4, 5},
			{1, 2, 3, 4000, 4001, 9999},
			{0, 2, 4, 6},
		} {
			got, err := decodeIndexRanges(encodeIndexRanges(indices))
			require.NoError(t, err)
			require.Equal(t, indices, got)
		}
	})

	t.Run("empty", func(t *testing.T) {
		got, err := decodeIndexRanges(nil)
		require.NoError(t, err)
		require.Nil(t, got)
	})

	t.Run("rejects malformed", func(t *testing.T) {
		for _, ranges := range [][]string{
			{"abc"},
			{""},
			{"1-"},
			{"-3"},         // parses as empty start
			{"3--5"},       // negative end
			{"5-1"},        // end below start
			{"1-3", "2-6"}, // overlapping
			{"1-3", "4-6"}, // adjacent: encoder would have merged, not canonical
			{"5-9", "1-3"}, // descending
			{"1-3", "3"},   // duplicate boundary
		} {
			_, err := decodeIndexRanges(ranges)
			require.Error(t, err, "ranges %v should be rejected", ranges)
		}
	})

	t.Run("caps expansion size", func(t *testing.T) {
		_, err := decodeIndexRanges([]string{fmt.Sprintf("1-%d", maxDecodedIndices+10)})
		require.Error(t, err)
		require.Contains(t, err.Error(), "limit")
	})
}

func minimalPersistedState(indices, holders []int) *PersistedState {
	return &PersistedState{
		RPCURL:            "http://localhost:8000",
		NetworkPassphrase: "Test SDF Network ; September 2015",
		FeePayerHash:      "abc123",
		AccountIndices:    indices,
		SACHolderIndices:  holders,
	}
}

func TestSaveWritesRangesAndLoadRestoresIndices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// Simulate a post-partial-teardown pool: hole in the middle of the tail.
	indices := make([]int, 0, 4500)
	for i := 1; i <= 4500; i++ {
		if i >= 4000 && i < 4100 { // a failed middle batch left a gap
			continue
		}
		indices = append(indices, i)
	}
	holders := make([]int, 0, 900)
	for i := 1; i <= 900; i++ {
		holders = append(holders, i)
	}
	ps := minimalPersistedState(indices, holders)
	require.NoError(t, ps.Save(path))

	// Receiver must not be mutated by Save.
	require.Equal(t, indices, ps.AccountIndices)
	require.Nil(t, ps.AccountRanges)

	// The raw file must carry the compact form only.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	require.NotContains(t, onDisk, "account_indices")
	require.NotContains(t, onDisk, "sac_holder_indices")
	require.Equal(t, []any{"1-3999", "4100-4500"}, onDisk["account_ranges"])
	require.Equal(t, []any{"1-900"}, onDisk["sac_holder_ranges"])
	// The whole point: the file stays tiny even for thousands of accounts.
	require.Less(t, len(raw), 2048, "range-encoded state file should be under 2 KiB")

	// Loading restores the exact index lists and clears the range fields.
	loaded, err := NewPersistedState(path)
	require.NoError(t, err)
	require.Equal(t, indices, loaded.AccountIndices)
	require.Equal(t, holders, loaded.SACHolderIndices)
	require.Nil(t, loaded.AccountRanges)
	require.Nil(t, loaded.SACHolderRanges)
}

func TestLoadAcceptsLegacyIndexArrays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{
		"rpc_url": "http://localhost:8000",
		"network_passphrase": "Test SDF Network ; September 2015",
		"fee_payer_hash": "abc123",
		"account_indices": [1, 2, 3, 7, 9],
		"sac_holder_indices": [1, 2]
	}`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o600))

	loaded, err := NewPersistedState(path)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3, 7, 9}, loaded.AccountIndices)
	require.Equal(t, []int{1, 2}, loaded.SACHolderIndices)

	// First save upgrades the file to the range form in place.
	require.NoError(t, loaded.Save(path))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	require.NotContains(t, onDisk, "account_indices")
	require.Equal(t, []any{"1-3", "7", "9"}, onDisk["account_ranges"])
}

func TestLoadRejectsBothEncodings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	conflicted := `{
		"rpc_url": "http://localhost:8000",
		"network_passphrase": "Test SDF Network ; September 2015",
		"fee_payer_hash": "abc123",
		"account_indices": [1, 2, 3],
		"account_ranges": ["1-3"]
	}`
	require.NoError(t, os.WriteFile(path, []byte(conflicted), 0o600))
	_, err := NewPersistedState(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "both")
}

func TestSaveLoadRoundTripEmptyPool(t *testing.T) {
	// A fully-torn-down state (zero accounts) must round-trip without
	// producing empty range arrays or failing validation.
	path := filepath.Join(t.TempDir(), "state.json")
	ps := minimalPersistedState(nil, nil)
	require.NoError(t, ps.Save(path))
	loaded, err := NewPersistedState(path)
	require.NoError(t, err)
	require.Empty(t, loaded.AccountIndices)
	require.Empty(t, loaded.SACHolderIndices)
}
