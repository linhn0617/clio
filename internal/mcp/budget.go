package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

// maxCharsOf reads the max_chars parameter, clamped to [minMaxChars,
// maxMaxChars]; absent or non-positive means defaultMaxChars.
func maxCharsOf(req mcp.CallToolRequest) int {
	return clampRange(req.GetInt("max_chars", defaultMaxChars), defaultMaxChars, minMaxChars, maxMaxChars)
}

// capText keeps the first limit code points of s and appends a marker saying
// how many were cut. A negative limit means "no cap".
func capText(s string, limit int) string {
	if limit < 0 {
		return s
	}
	n := utf8.RuneCountInString(s)
	if n <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit]) + fmt.Sprintf("… [truncated %d chars]", n-limit)
}

// measure is the budget's unit: Unicode code points of the serialized JSON,
// escapes included.
func measure(m map[string]any) int {
	b, err := json.Marshal(m)
	if err != nil {
		return 0 // unreachable for our plain payloads; fitBudget reports it
	}
	return utf8.RuneCount(b)
}

// jsonText wraps a payload as a text-only tool result. The mcp-go helper
// NewToolResultJSON also attaches the payload as structuredContent, which
// would put a second copy of every response on the wire; none of our tools
// declares an output schema, so the text content (what Claude Code reads and
// applies its 50,000-character spill threshold to) is all clients need.
func jsonText(b []byte) *mcp.CallToolResult {
	return mcp.NewToolResultText(string(b))
}

// fitBudget renders a response of n rows within maxChars code points of
// serialized JSON, measured on the real output rather than estimated.
//
// render(k, textCap) must build the whole payload with only the first k rows
// (it decides which k rows when "first" is not right, e.g. read_session
// windows); textCap >= 0 asks it to cut each text field of the kept rows to
// that many code points via capText, and < 0 means no cap. fitBudget adds
// "truncated" and "next" itself whenever rows or text were dropped, so those
// fields count towards the size; hint(kept, total) words the row-dropping case.
//
// Order of attack, each step stopping as soon as the result fits: (1) all
// rows; (2) fewer rows (binary search, size is monotone in k); (3) one row
// with its text cut (binary search on the cap), because an empty page could
// never make progress; (4) if even that overflows, the fixed fields alone
// exceed the budget — the one row is returned anyway and the hint says so.
func fitBudget(n, maxChars int, render func(k, textCap int) map[string]any, hint func(kept, total int) string) *mcp.CallToolResult {
	build := func(k, textCap int) (map[string]any, int) {
		m := render(k, textCap)
		if k < n {
			m["truncated"] = true
			m["next"] = hint(k, n)
		} else if textCap >= 0 {
			m["truncated"] = true
			if m["next"] == nil {
				m["next"] = fmt.Sprintf("row content was cut to fit max_chars=%d; read the full text with a narrower query, or raise max_chars (max %d)", maxChars, maxMaxChars)
			}
		}
		return m, measure(m)
	}
	finish := func(m map[string]any) *mcp.CallToolResult {
		b, err := json.Marshal(m)
		if err != nil {
			return mcp.NewToolResultError(err.Error())
		}
		return jsonText(b)
	}

	if m, size := build(n, -1); size <= maxChars {
		return finish(m)
	}
	best, lo, hi := 0, 1, n-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if _, size := build(mid, -1); size <= maxChars {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	if best > 0 {
		m, _ := build(best, -1)
		return finish(m)
	}

	k := min(n, 1)
	bestCap, lo, hi := -1, 0, maxChars
	for k > 0 && lo <= hi {
		mid := (lo + hi) / 2
		if _, size := build(k, mid); size <= maxChars {
			bestCap, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	if bestCap >= 0 {
		m, _ := build(k, bestCap)
		return finish(m)
	}
	m, _ := build(k, 0)
	m["truncated"] = true
	m["next"] = fmt.Sprintf("fixed fields exceed max_chars=%d even with row text removed; raise max_chars (max %d) or narrow the query", maxChars, maxMaxChars)
	return finish(m)
}

// fitRows is fitBudget for the tools whose payload is "fixed fields plus one
// slice of rows kept from the front". build wraps a row slice into the
// payload; capRow (may be nil) cuts a row's free-text field for the one-row
// fallback; noun and advice word the dropped-rows hint.
func fitRows[T any](rows []T, maxChars int, capRow func(r *T, textCap int), build func(rows []T) map[string]any, noun, advice string) *mcp.CallToolResult {
	return fitBudget(len(rows), maxChars, func(k, textCap int) map[string]any {
		kept := rows[:k]
		if textCap >= 0 && capRow != nil {
			kept = slices.Clone(kept)
			for i := range kept {
				capRow(&kept[i], textCap)
			}
		}
		return build(kept)
	}, func(kept, total int) string {
		return fmt.Sprintf("showing %d of %d %s to fit max_chars=%d; %s, or raise max_chars (max %d)", kept, total, noun, maxChars, advice, maxMaxChars)
	})
}
