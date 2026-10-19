package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// maxCharsOf reads the max_chars parameter, clamped to [minMaxChars,
// maxMaxChars]; absent or non-positive means defaultMaxChars.
func maxCharsOf(req mcp.CallToolRequest) int {
	return clampRange(req.GetInt("max_chars", defaultMaxChars), defaultMaxChars, minMaxChars, maxMaxChars)
}

// capText keeps the first limit characters of s (UTF-16 code units, the
// budget's unit, never splitting a character) and appends a marker saying how
// many were cut. A negative limit means "no cap".
func capText(s string, limit int) string {
	if limit < 0 {
		return s
	}
	n := 0
	for _, r := range s {
		n += runeUnits(r)
	}
	if n <= limit {
		return s
	}
	kept, end := 0, 0
	for i, r := range s {
		if kept+runeUnits(r) > limit {
			end = i
			break
		}
		kept += runeUnits(r)
	}
	return s[:end] + fmt.Sprintf("… [truncated %d chars]", n-kept)
}

// runeUnits is r's length in UTF-16 code units: 2 for an astral character
// (most emoji), 1 otherwise.
func runeUnits(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// utf16Units is the budget's unit: the UTF-16 code units of b, which must be
// valid UTF-8 (json.Marshal output always is). That is how JavaScript counts
// string length, and so most likely how Claude Code applies its 50,000
// character threshold; an emoji counts 2 where a code-point count would say 1.
// One unit per non-continuation byte, plus one more per 4-byte sequence.
func utf16Units(b []byte) int {
	n := 0
	for _, c := range b {
		if c&0xC0 != 0x80 {
			n++
		}
		if c >= 0xF0 {
			n++
		}
	}
	return n
}

// measure is the budget's size of a payload: UTF-16 code units of its
// serialized JSON, escapes included.
func measure(m map[string]any) int {
	b, err := json.Marshal(m)
	if err != nil {
		return 0 // unreachable for our plain payloads; fitBudget reports it
	}
	return utf16Units(b)
}

// jsonText wraps a payload as a text-only tool result. The mcp-go helper
// NewToolResultJSON also attaches the payload as structuredContent, which
// would put a second copy of every response on the wire; none of our tools
// declares an output schema, so the text content (what Claude Code reads and
// applies its 50,000-character spill threshold to) is all clients need.
func jsonText(b []byte) *mcp.CallToolResult {
	return mcp.NewToolResultText(string(b))
}

// fitBudget renders a response of n rows within maxChars UTF-16 code units
// of serialized JSON, measured on the exact text sent rather than estimated.
//
// render(k, textCap) must build the whole payload with only the first k rows
// (it decides which k rows when "first" is not right, e.g. read_session
// windows); textCap >= 0 asks it to cut each text field of the kept rows to
// that many units via capText, and < 0 means no cap. A "next" string render
// sets is a note of its own and is kept. fitBudget adds "truncated" and
// "next" itself whenever rows or text were dropped, joining every reason, so
// those fields count towards the size; hint(kept, total) words the
// row-dropping case. cuttable says whether rows have text render can cut.
// shed (may be nil) runs once if all rows overflow, to drop parts outside the
// rows that render reads (read_session's subagents) before any row goes; it
// reports whether it dropped anything.
//
// Order of attack, each step stopping as soon as the result fits: (1) all
// rows, retried after a shed; (2) fewer rows (binary search, size is monotone
// in k); (3) one row with its text cut (binary search on the cap), skipped
// when rows have no cuttable text; (4) if even that overflows, one row's fixed
// fields alone exceed the budget, so no rows are returned and the hint says so.
func fitBudget(n, maxChars int, cuttable bool, render func(k, textCap int) map[string]any, hint func(kept, total int) string, shed func() bool) *mcp.CallToolResult {
	// build serializes k rows; note, when set, replaces the dropped-rows hint.
	build := func(k, textCap int, note string) ([]byte, int, error) {
		m := render(k, textCap)
		var parts []string
		if s, _ := m["next"].(string); s != "" {
			parts = append(parts, s)
		}
		if note != "" {
			parts = append(parts, note)
		} else if k < n {
			parts = append(parts, hint(k, n))
		}
		if textCap >= 0 {
			parts = append(parts, fmt.Sprintf("row content was cut to fit max_chars=%d; read the full text with a narrower query, or raise max_chars (max %d)", maxChars, maxMaxChars))
		}
		if len(parts) > 0 {
			m["truncated"] = true
			m["next"] = strings.Join(parts, "; ")
		}
		b, err := json.Marshal(m)
		return b, utf16Units(b), err
	}
	finish := func(b []byte, err error) *mcp.CallToolResult {
		if err != nil {
			return mcp.NewToolResultError(err.Error())
		}
		return jsonText(b)
	}
	// search returns the serialized result for the largest v in [lo, hi]
	// whose size fits, or nil when none does.
	search := func(lo, hi int, size func(v int) ([]byte, int, error)) ([]byte, error) {
		var best []byte
		for lo <= hi {
			mid := (lo + hi) / 2
			b, n, err := size(mid)
			if err != nil {
				return nil, err
			}
			if n <= maxChars {
				best, lo = b, mid+1
			} else {
				hi = mid - 1
			}
		}
		return best, nil
	}

	if b, size, err := build(n, -1, ""); err != nil || size <= maxChars {
		return finish(b, err)
	}
	if shed != nil && shed() {
		if b, size, err := build(n, -1, ""); err != nil || size <= maxChars {
			return finish(b, err)
		}
	}
	b, err := search(1, n-1, func(k int) ([]byte, int, error) { return build(k, -1, "") })
	if err != nil || b != nil {
		return finish(b, err)
	}
	if n > 0 && cuttable {
		b, err := search(0, maxChars, func(c int) ([]byte, int, error) { return build(1, c, "") })
		if err != nil || b != nil {
			return finish(b, err)
		}
	}
	note := fmt.Sprintf("fixed fields exceed max_chars=%d even for a single row, so no rows are returned; raise max_chars (max %d) or narrow the query", maxChars, maxMaxChars)
	if n == 0 {
		note = fmt.Sprintf("fixed fields exceed max_chars=%d even with no rows; raise max_chars (max %d) or narrow the query", maxChars, maxMaxChars)
	}
	b, _, err = build(0, -1, note)
	return finish(b, err)
}

// fitRows is fitBudget for the tools whose payload is "fixed fields plus one
// slice of rows kept from the front". build wraps a row slice into the
// payload; capRow (nil when rows have no free text) cuts a row's free-text
// field for the one-row fallback; noun and advice word the dropped-rows hint.
func fitRows[T any](rows []T, maxChars int, capRow func(r *T, textCap int), build func(rows []T) map[string]any, noun, advice string) *mcp.CallToolResult {
	return fitBudget(len(rows), maxChars, capRow != nil, func(k, textCap int) map[string]any {
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
	}, nil)
}
