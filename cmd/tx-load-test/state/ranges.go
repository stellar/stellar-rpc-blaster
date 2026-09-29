package state

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// maxDecodedIndices caps how many account indices a ranges list may expand to.
// This guards against a typo'd or hostile range ("1-4000000000") allocating
// gigabytes on load. The cap is far above any realistic pool size -- benchmark
// economics (reserves, rent) bind orders of magnitude earlier.
const maxDecodedIndices = 1 << 20

// encodeIndexRanges compresses a set of derivation indices into the compact
// range form persisted on disk: contiguous runs render as "a-b", singletons as
// "a". The input is treated as a SET: it is copied, sorted ascending, and
// deduplicated, so arbitrary in-memory ordering does not survive encoding.
// Returns nil for an empty input so the JSON field is omitted entirely.
func encodeIndexRanges(indices []int) []string {
	if len(indices) == 0 {
		return nil
	}
	sorted := slices.Clone(indices)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)

	ranges := make([]string, 0, 4)
	start, end := sorted[0], sorted[0]
	flush := func() {
		if start == end {
			ranges = append(ranges, strconv.Itoa(start))
			return
		}
		ranges = append(ranges, strconv.Itoa(start)+"-"+strconv.Itoa(end))
	}
	for _, idx := range sorted[1:] {
		if idx == end+1 {
			end = idx
			continue
		}
		flush()
		start, end = idx, idx
	}
	flush()
	return ranges
}

// decodeIndexRanges expands the compact on-disk range form back into a flat,
// ascending index list. It validates canonical form: each entry is "a" or
// "a-b" with 0 <= a <= b, entries are strictly ascending, and ranges do not
// touch or overlap (the encoder would have merged them). The expansion size is
// capped by maxDecodedIndices.
func decodeIndexRanges(ranges []string) ([]int, error) {
	if len(ranges) == 0 {
		return nil, nil
	}
	indices := make([]int, 0, len(ranges))
	prevEnd := -2 // allows a first range starting at 0
	for i, r := range ranges {
		start, end, err := parseIndexRange(r)
		if err != nil {
			return nil, fmt.Errorf("range[%d] %q: %w", i, r, err)
		}
		if start <= prevEnd+1 {
			return nil, fmt.Errorf("range[%d] %q is not in canonical form: ranges must be ascending and non-adjacent (previous range ended at %d)", i, r, prevEnd)
		}
		if len(indices)+(end-start+1) > maxDecodedIndices {
			return nil, fmt.Errorf("range[%d] %q expands the index list past the %d-entry limit", i, r, maxDecodedIndices)
		}
		for idx := start; idx <= end; idx++ {
			indices = append(indices, idx)
		}
		prevEnd = end
	}
	return indices, nil
}

func parseIndexRange(r string) (int, int, error) {
	lo, hi, isRange := strings.Cut(r, "-")
	start, err := strconv.Atoi(lo)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid start index %q", lo)
	}
	if !isRange {
		return start, start, nil
	}
	end, err := strconv.Atoi(hi)
	if err != nil || end < 0 {
		return 0, 0, fmt.Errorf("invalid end index %q", hi)
	}
	if end < start {
		return 0, 0, fmt.Errorf("end %d is below start %d", end, start)
	}
	return start, end, nil
}
