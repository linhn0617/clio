package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// maxCharsOf reads the max_chars parameter, clamped to [minMaxChars,
// maxMaxChars]; absent or non-positive means defaultMaxChars.
func maxCharsOf(req mcp.CallToolRequest) int {
	return clampRange(req.GetInt("max_chars", defaultMaxChars), defaultMaxChars, minMaxChars, maxMaxChars)
}

// fitBudget renders a response of n rows within maxChars serialized
// characters. render(k) must build the complete payload carrying only the
// first k rows; when k < n it must also set "truncated" and "next" itself, so
// those fields are counted in the size.
//
// The size is measured on the real json.Marshal output (escapes included)
// rather than estimated, by binary search for the largest k that fits — row
// size is monotone in k, so that is O(log n) renders. At least one row is
// always returned: if even one row exceeds the budget it is returned anyway
// with truncated=true, because an empty page could never make progress.
// oversized reports that case.
func fitBudget(n, maxChars int, render func(k int) map[string]any) (out map[string]any, kept int, oversized bool) {
	size := func(m map[string]any) int {
		b, err := json.Marshal(m)
		if err != nil {
			return 0 // unreachable for our plain payloads; NewToolResultJSON reports it
		}
		return len(b)
	}
	if full := render(n); size(full) <= maxChars {
		return full, n, false
	}
	best, lo, hi := 0, 1, n-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if size(render(mid)) <= maxChars {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	if best > 0 {
		return render(best), best, false
	}
	k := min(n, 1)
	m := render(k)
	if m["truncated"] == nil {
		// A lone row that is itself over budget: nothing was dropped, but the
		// caller must still be told the response exceeds what it asked for.
		m["truncated"] = true
		m["next"] = fmt.Sprintf("a single row exceeds max_chars=%d and is returned whole; raise max_chars (max %d) or narrow the query", maxChars, maxMaxChars)
	}
	return m, k, true
}
