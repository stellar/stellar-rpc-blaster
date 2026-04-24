package parameters

import (
	"testing"
)

// TestConfiguredLimitsRespected loads the real seed data and verifies that
// BuildEndpointParams generates request parameters that respect the
// configured limits from config.example.toml.
func TestConfiguredLimitsRespected(t *testing.T) {
	params, err := GetParameters("../../../output/seed.json")
	if err != nil {
		t.Fatalf("failed to load seed data: %v", err)
	}

	lr := params.Output.LedgerRange
	t.Logf("Seed ledger range: %d – %d (span %d)", lr.First, lr.Last, lr.Last-lr.First)

	const numBodies = 200 // generate enough to get a good sample

	tests := []struct {
		endpoint string
		limit    uint32 // from config.example.toml
	}{
		{"getTransactions", 50},
		{"getLedgers", 1},
		{"getEvents", 10},
	}

	for _, tc := range tests {
		t.Run(tc.endpoint, func(t *testing.T) {
			results, err := BuildEndpointParams(tc.endpoint, numBodies, params)
			if err != nil {
				t.Fatalf("BuildEndpointParams failed: %v", err)
			}
			if len(results) == 0 {
				t.Fatal("expected at least one result")
			}

			// getEvents treats endLedger as exclusive, so its run-mode builder allows
			// start+limit / endLedger to reach lr.Last+1; other endpoints stay at lr.Last.
			ledgerUpperBound := lr.Last
			if tc.endpoint == "getEvents" {
				ledgerUpperBound = lr.Last + 1
			}

			for i, entry := range results {
				checkPaginationLimit(t, i, entry, tc.limit, ledgerUpperBound)

				if tc.endpoint == "getEvents" {
					checkEventsLedgerWindow(t, i, entry, tc.limit, ledgerUpperBound)
				}
			}
		})
	}
}

func checkPaginationLimit(t *testing.T, idx int, entry map[string]any, configLimit, ledgerUpperBound uint32) {
	t.Helper()
	pagination, ok := entry["pagination"].(map[string]any)
	if !ok {
		t.Errorf("[%d] missing or invalid pagination map", idx)
		return
	}
	limit, ok := pagination["limit"].(uint)
	if !ok {
		t.Errorf("[%d] pagination.limit is not uint: %T", idx, pagination["limit"])
		return
	}
	if limit > uint(configLimit) {
		t.Errorf("[%d] pagination.limit %d exceeds configured limit %d", idx, limit, configLimit)
	}
	if limit == 0 {
		t.Errorf("[%d] pagination.limit is 0", idx)
	}

	// When cursor is present, startLedger should be removed
	if _, hasCursor := pagination["cursor"]; hasCursor {
		if _, hasStart := entry["startLedger"]; hasStart {
			t.Errorf("[%d] cursor is set but startLedger was not removed", idx)
		}
	}

	// startLedger + pagination.limit should not exceed the (endpoint-specific) upper bound
	if start, hasStart := entry["startLedger"].(uint32); hasStart {
		if start+uint32(limit) > ledgerUpperBound {
			t.Errorf("[%d] startLedger %d + pagination.limit %d = %d exceeds upper bound %d",
				idx, start, limit, start+uint32(limit), ledgerUpperBound)
		}
	}
}

func checkEventsLedgerWindow(t *testing.T, idx int, entry map[string]any, configLimit, ledgerUpperBound uint32) {
	t.Helper()
	// When cursor-based, startLedger/endLedger are deleted — skip window check
	if _, hasStart := entry["startLedger"]; !hasStart {
		return
	}

	startRaw, ok := entry["startLedger"].(uint32)
	if !ok {
		t.Errorf("[%d] startLedger not uint32: %T", idx, entry["startLedger"])
		return
	}
	endRaw, ok := entry["endLedger"].(uint32)
	if !ok {
		t.Errorf("[%d] endLedger not uint32: %T", idx, entry["endLedger"])
		return
	}

	window := endRaw - startRaw
	if window > uint32(configLimit) {
		t.Errorf("[%d] ledger window %d (start=%d end=%d) exceeds configured limit %d",
			idx, window, startRaw, endRaw, configLimit)
	}
	if endRaw > ledgerUpperBound {
		t.Errorf("[%d] endLedger %d exceeds upper bound %d", idx, endRaw, ledgerUpperBound)
	}
}
