package mcp

import (
	"encoding/json"
	"fmt"
	"io"
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
// render(k, textCap) must return a new map on every call (fitBudget writes
// "truncated" and "next" into it) and build the whole payload with only the first k rows
// (it decides which k rows when "first" is not right, e.g. read_session
// windows); textCap >= 0 asks it to cut each text field of the kept rows to
// that many units via capText, and < 0 means no cap. A "next" string render
// sets is a note of its own and is kept. fitBudget adds "truncated" and
// "next" itself whenever rows or text were dropped, joining every reason, so
// those fields count towards the size; hint(kept, total) words the
// row-dropping case. cuttable says whether rows have text render can cut.
// shed (may be nil) runs once if all rows overflow, to drop parts outside the
// rows that render reads (read_session's subagents) before any row goes; it
// reports whether it dropped anything. Its fits(k) argument says whether k
// uncut rows fit as they would be sent, truncated/next hints included;
// zeroSize() is the zero-row payload's size with step (1b)'s cut applied (no
// hints), which shed must use rather than measuring render itself.
//
// Order of attack, each step stopping as soon as the result fits: (1) all
// rows; (1b) fields outside the rows (an echoed question, a long path or
// title) are cut, before the shed and before any row is dropped, so they take
// what cannot be cut plus a quarter of the rest of the budget; (1c) all rows after a shed; (2) fewer
// rows (binary search, size is monotone in k); (3) one row with its text cut
// (binary search on the cap), skipped when rows have no cuttable text; (4) if
// even that overflows, one row's own fields exceed the budget, so no rows are
// returned and the hint says so.
func fitBudget(n, maxChars int, cuttable bool, render func(k, textCap int) map[string]any, hint func(kept, total int) string, shed func(fits func(k int) bool, zeroSize func() int) bool) *mcp.CallToolResult {
	// Once step (1b) ran, fixedCap >= 0 and the fields named in fixedKeys (those
	// outside the rows) are cut to it in every payload build renders.
	fixedCap := -1
	var fixedKeys []string
	// build serializes k rows; note, when set, replaces the dropped-rows hint.
	// view is render with the step (1b) cut applied; cutPaths names what it cut.
	view := func(k, textCap int) (map[string]any, []string, error) {
		m := render(k, textCap)
		var cutPaths []string
		if fixedCap >= 0 {
			for _, key := range fixedKeys {
				raw, err := json.Marshal(m[key])
				if err != nil {
					return nil, nil, err
				}
				out, cut, err := capJSON(raw, fixedCap, key)
				if err != nil {
					return nil, nil, err
				}
				m[key] = out
				cutPaths = append(cutPaths, cut...)
			}
		}
		return m, cutPaths, nil
	}
	build := func(k, textCap int, note string) ([]byte, int, error) {
		m, cutPaths, err := view(k, textCap)
		if err != nil {
			return nil, 0, err
		}
		var parts []string
		if s, _ := m["next"].(string); s != "" {
			parts = append(parts, s)
		}
		if len(cutPaths) > 0 {
			slices.Sort(cutPaths)
			cutPaths = slices.Compact(cutPaths)
			parts = append(parts, fmt.Sprintf("long fields were cut to fit max_chars=%d: %s", maxChars, strings.Join(cutPaths, ", ")))
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
	keys, c, err := fixedFieldCap(n, maxChars, render)
	if err != nil {
		return finish(nil, err)
	}
	fixedKeys, fixedCap = keys, c
	if fixedCap >= 0 {
		if b, size, err := build(n, -1, ""); err != nil || size <= maxChars {
			return finish(b, err)
		}
	}
	fits := func(k int) bool {
		_, size, err := build(k, -1, "")
		return err != nil || size <= maxChars // an error surfaces on the final build
	}
	zeroSize := func() int { // the zero-row payload as sent, fields cut, no hints
		m, _, err := view(0, -1)
		if err != nil {
			return 0 // the error surfaces on the final build
		}
		return measure(m)
	}
	if shed != nil && shed(fits, zeroSize) {
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

// fixedFieldCap is fitBudget's step (1b). The fields outside the rows are the
// top-level keys, other than lists (read_session's subagents have their own
// shedding) and "next", whose value is the same with no rows and with all n.
// The target is floor (their size with every cuttable string at its marker)
// plus a quarter of the rest of the budget; when the fields exceed it, it
// returns them and the largest cap that brings them within it (cap 0 when floor
// alone fills the budget); otherwise cap -1. Their size is measured on their own, so it never shrinks as
// the cap grows (capJSON only replaces a string with a shorter one) and the
// binary search finds the largest cap.
func fixedFieldCap(n, maxChars int, render func(k, textCap int) map[string]any) ([]string, int, error) {
	zero, full := render(0, -1), render(n, -1)
	raws := map[string][]byte{}
	var keys []string
	for key, v := range zero {
		if key == "next" {
			continue
		}
		zb, err := json.Marshal(v)
		if err != nil {
			return nil, -1, err
		}
		if len(zb) > 0 && zb[0] == '[' {
			continue
		}
		if fb, err := json.Marshal(full[key]); err == nil && string(fb) == string(zb) {
			keys = append(keys, key)
			raws[key] = zb
		}
	}
	slices.Sort(keys)
	size := func(c int) (int, error) {
		total := 0
		for _, key := range keys {
			out := json.RawMessage(raws[key])
			if c >= 0 {
				var err error
				if out, _, err = capJSON(raws[key], c, key); err != nil {
					return 0, err
				}
			}
			total += jsonUnits(key) + 1 + utf16Units(out)
		}
		return total, nil
	}
	// The cut can never go below floor: ids, notes, timestamps, keys, numbers
	// and strings too short to cut. The fields get floor plus a quarter of the
	// rest of the budget, so rows and hints keep three quarters of what the
	// uncuttable part leaves (with a small floor, about a quarter of the budget;
	// read_session's subagent list is held to half the budget with these fields
	// included, so it keeps at least the rest of that half).
	floor, err := size(0)
	if err != nil {
		return nil, -1, err
	}
	if floor >= maxChars {
		return keys, 0, nil // nothing fits: send the smallest form
	}
	target := floor + (maxChars-floor)/4
	if s, err := size(-1); err != nil || s <= target {
		return nil, -1, err
	}
	lo, hi, found := 0, maxChars, 0
	for lo <= hi {
		mid := (lo + hi) / 2
		s, err := size(mid)
		if err != nil {
			return nil, -1, err
		}
		if s <= target {
			found, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	return keys, found, nil
}

// capJSON cuts every string value in the JSON value raw to textCap units by
// capText, where that shortens it as sent; every other token (keys, uncut
// strings, numbers) is written back byte for byte, so order, numbers and
// escapes are kept exactly. Values under keys naming ids, notes or hints stay
// whole. It returns the dotted paths of the fields it cut.
func capJSON(raw []byte, textCap int, path string) (json.RawMessage, []string, error) {
	type frame struct {
		obj, wantKey, first bool
		key, path           string
		name                string // the key holding this container: names its array elements
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var out strings.Builder
	var cut []string
	st := []frame{}
	done := func() { // a value just ended in the innermost container
		if len(st) > 0 && st[len(st)-1].obj {
			st[len(st)-1].wantKey = true
		}
	}
	// orig is the token's own bytes in raw (separators and whitespace before it
	// skipped), written back verbatim for anything left uncut.
	var start int64
	orig := func() string {
		seg := string(raw[start:dec.InputOffset()])
		return strings.TrimLeft(seg, " \t\r\n,:")
	}
	for {
		start = dec.InputOffset()
		t, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if d, ok := t.(json.Delim); ok && (d == '}' || d == ']') {
			out.WriteByte(byte(d))
			st = st[:len(st)-1]
			done()
			continue
		}
		cur, key := path, path // a top-level value is named by its key
		if len(st) > 0 {
			f := &st[len(st)-1]
			cur = f.path
			if f.obj && f.wantKey {
				if !f.first {
					out.WriteByte(',')
				}
				f.first = false
				k, _ := t.(string)
				out.WriteString(orig())
				out.WriteByte(':')
				f.key, f.wantKey = k, false
				continue
			}
			if f.obj {
				key = f.key
				cur = f.path + "." + key
			} else {
				key = f.name
				if !f.first {
					out.WriteByte(',')
				}
				f.first = false
			}
		}
		switch v := t.(type) {
		case json.Delim:
			out.WriteByte(byte(v))
			st = append(st, frame{obj: v == '{', wantKey: v == '{', first: true, path: cur, name: key})
			continue
		case string:
			c := capText(v, textCap)
			if !keepWhole(key) && c != v && jsonUnits(c) < utf16Units([]byte(orig())) {
				b, _ := json.Marshal(c)
				out.Write(b)
				if !slices.Contains(cut, cur) { // a list's elements share one path
					cut = append(cut, cur)
				}
			} else {
				out.WriteString(orig())
			}
		default: // numbers, booleans, null
			out.WriteString(orig())
		}
		done()
	}
	return json.RawMessage(out.String()), cut, nil
}

// keepWhole reports whether a string under key (any case) must not be cut:
// ids (a caller reads by them: uuid keys, id, parent_session), notes and hints
// (guidance the caller follows), and timestamps (`*_at`, timestamp: a cut one
// is meaningless).
func keepWhole(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "uuid") || k == "id" || k == "parent_session" || k == "note" || k == "next" ||
		strings.HasSuffix(k, "_at") || k == "timestamp"
}

// jsonUnits is a string's budget size as sent: UTF-16 code units of its JSON
// encoding, quotes and escapes included.
func jsonUnits(s string) int {
	b, _ := json.Marshal(s)
	return utf16Units(b)
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
