package main

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// encodeAccount marshals an AccountEntry into the base64 LedgerEntryData form
// that getLedgerEntries returns, so we exercise the real decode path.
func encodeAccount(t *testing.T, acct xdr.AccountEntry) string {
	t.Helper()
	// A real account entry always carries a key; the zero-value AccountId has a
	// nil Ed25519 pointer that panics on marshal. The balance/reserve math is
	// independent of the ID, so any valid key works.
	var ed xdr.Uint256
	acct.AccountId = xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &ed}
	data := xdr.LedgerEntryData{Type: xdr.LedgerEntryTypeAccount, Account: &acct}
	b64, err := xdr.MarshalBase64(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b64
}

func TestAccountInfoFromEntryXDR(t *testing.T) {
	const baseReserve = int64(5_000_000) // 0.5 XLM

	tests := []struct {
		name           string
		build          func() xdr.AccountEntry
		wantMinReserve int64
		wantFree       int64
	}{
		{
			name: "bare account, no subentries",
			build: func() xdr.AccountEntry {
				return xdr.AccountEntry{Balance: 30_000_000} // 3 XLM
			},
			wantMinReserve: 2 * baseReserve,            // 1.0 XLM
			wantFree:       30_000_000 - 2*baseReserve, // 2.0 XLM
		},
		{
			name: "three trustlines (typical benchmark account)",
			build: func() xdr.AccountEntry {
				return xdr.AccountEntry{Balance: 30_000_000, NumSubEntries: 3}
			},
			wantMinReserve: 5 * baseReserve,            // (2+3)*0.5 = 2.5 XLM
			wantFree:       30_000_000 - 5*baseReserve, // 0.5 XLM
		},
		{
			name: "selling liabilities reduce free but not reserve",
			build: func() xdr.AccountEntry {
				return xdr.AccountEntry{
					Balance:       30_000_000,
					NumSubEntries: 3,
					Ext: xdr.AccountEntryExt{
						V:  1,
						V1: &xdr.AccountEntryExtensionV1{Liabilities: xdr.Liabilities{Selling: 1_000_000}},
					},
				}
			},
			wantMinReserve: 5 * baseReserve,
			wantFree:       30_000_000 - 5*baseReserve - 1_000_000,
		},
		{
			name: "sponsorship: sponsoring raises reserve, sponsored lowers it",
			build: func() xdr.AccountEntry {
				return xdr.AccountEntry{
					Balance:       30_000_000,
					NumSubEntries: 2,
					Ext: xdr.AccountEntryExt{
						V: 1,
						V1: &xdr.AccountEntryExtensionV1{
							Ext: xdr.AccountEntryExtensionV1Ext{
								V:  2,
								V2: &xdr.AccountEntryExtensionV2{NumSponsoring: 3, NumSponsored: 1},
							},
						},
					},
				}
			},
			// (2 + 2 + 3 - 1) = 6 subentry-equivalents
			wantMinReserve: 6 * baseReserve,
			wantFree:       30_000_000 - 6*baseReserve,
		},
		{
			name: "below reserve floor yields negative free",
			build: func() xdr.AccountEntry {
				return xdr.AccountEntry{Balance: 5_000_000, NumSubEntries: 3} // 0.5 XLM, needs 2.5
			},
			wantMinReserve: 5 * baseReserve,
			wantFree:       5_000_000 - 5*baseReserve, // negative
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b64 := encodeAccount(t, tc.build())
			info, err := accountInfoFromEntryXDR(b64, baseReserve)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if info.minReserve != tc.wantMinReserve {
				t.Errorf("minReserve = %d, want %d", info.minReserve, tc.wantMinReserve)
			}
			if info.freeBalance != tc.wantFree {
				t.Errorf("freeBalance = %d, want %d", info.freeBalance, tc.wantFree)
			}
		})
	}
}

func TestAccountInfoFromEntryXDRRejectsNonAccount(t *testing.T) {
	// A trustline entry must be rejected, not silently treated as an account.
	var ed xdr.Uint256
	data := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeTrustline,
		TrustLine: &xdr.TrustLineEntry{
			AccountId: xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &ed},
			Asset:     xdr.TrustLineAsset{Type: xdr.AssetTypeAssetTypeNative},
			Balance:   100,
		},
	}
	b64, err := xdr.MarshalBase64(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := accountInfoFromEntryXDR(b64, 5_000_000); err == nil {
		t.Fatal("expected error for non-account entry, got nil")
	}
}

func TestPercentile(t *testing.T) {
	sorted := []int64{0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := []struct {
		p    float64
		want int64
	}{
		// nearest-rank via int(p*(n-1)); truncates down (p1 surfaces near-lowest).
		{0.0, 0}, {0.01, 0}, {0.5, 50}, {0.99, 90}, {1.0, 100},
	}
	for _, c := range cases {
		if got := percentile(sorted, c.p); got != c.want {
			t.Errorf("percentile(%.2f) = %d, want %d", c.p, got, c.want)
		}
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(nil) = %d, want 0", got)
	}
}

func TestXLMToStroops(t *testing.T) {
	cases := []struct {
		xlm  float64
		want int64
	}{
		{0.5, 5_000_000},
		{0.0000001, 1},
		{3.0, 30_000_000},
		{0, 0},
	}
	for _, c := range cases {
		if got := xlmToStroops(c.xlm); got != c.want {
			t.Errorf("xlmToStroops(%v) = %d, want %d", c.xlm, got, c.want)
		}
	}
}
