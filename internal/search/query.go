// Package search builds and ranks queries over the message index.
package search

import (
	"strings"
	"unicode/utf8"
)

// Options controls a search.
type Options struct {
	Query             string
	Since             int64 // unix seconds; 0 = no lower bound
	ProjectPrefix     string
	Role              string // "" = any
	Limit             int
	IncludeToolOutput bool
	Touched           string // restrict to sessions whose tool calls touched this path prefix
	Tool              string // restrict to sessions that used this tool (exact name)
	Ran               string // restrict to sessions that ran a command containing this substring
	MaxPerSession     int    // cap candidates per session in Retrieve (0 = no cap)
	Source            string // "" / "claude-code" (default) | "codex" | "all"
}

// Result is one matched message.
type Result struct {
	MessageID     int64
	Seq           int
	SessionUUID   string
	ProjectPath   string
	Role          string
	TS            int64
	Snippet       string
	Score         float64
	ParentSession string // the hit session's parent, when it is a subagent transcript
	AgentType     string // the hit session's subagent type, when it is a subagent
	Source        string // the hit session's originating tool: "claude-code" | "codex"
}

// terms splits a query into terms, honoring double-quoted phrases.
func terms(q string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// partitionTerms splits terms into long (>=3 runes) and short (<3 runes) groups.
func partitionTerms(ts []string) (long, short []string) {
	for _, t := range ts {
		if utf8.RuneCountInString(t) >= 3 {
			long = append(long, t)
		} else {
			short = append(short, t)
		}
	}
	return
}

// quotedTerms wraps each term as an operator-safe FTS5 phrase (embedded " doubled),
// neutralizing FTS operators. Shared by the AND (buildMatchQuery) and OR
// (buildAnyMatchQuery) builders so the escaping can't drift between them.
func quotedTerms(terms []string) []string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return parts
}

// buildMatchQuery turns terms into an operator-safe FTS5 MATCH expression: each
// term is a quoted phrase, joined by spaces (AND).
func buildMatchQuery(terms []string) string {
	return strings.Join(quotedTerms(terms), " ")
}

// likeSnippetWindow is the total rune length of a LIKE-fallback snippet.
const likeSnippetWindow = 160

// likeSnippetBefore is how many runes of context windowSnippet keeps before
// the first matched term, when there is enough content on both sides.
const likeSnippetBefore = 60

// windowSnippet returns a roughly likeSnippetWindow-rune slice of content,
// centered on the first occurrence of any term, with an ellipsis marking
// either edge when the window doesn't start/end at the content's edge. If no
// term position is found (shouldn't happen given the caller's AND'd LIKE
// predicate, but kept safe to call standalone), it falls back to a leading
// window, matching the previous substr(1,160) behavior. Operates on runes
// throughout so multi-byte UTF-8 (e.g. CJK) is never split mid-character.
func windowSnippet(content string, terms []string) string {
	runes := []rune(content)
	if len(runes) <= likeSnippetWindow {
		return content
	}

	start := 0
	if pos := firstTermRunePos(content, terms); pos >= 0 {
		start = pos - likeSnippetBefore
		if start < 0 {
			start = 0
		}
	}
	end := start + likeSnippetWindow
	if end > len(runes) {
		end = len(runes)
		start = end - likeSnippetWindow
		if start < 0 {
			start = 0
		}
	}

	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet = snippet + "…"
	}
	return snippet
}

// firstTermRunePos returns the rune index of the earliest occurrence of any
// term in content (the minimum over all terms that occur at all), or -1 if
// none occurs. Case-folding is ASCII-only via asciiLower, matching SQLite's
// own default LIKE case-insensitivity (which likeQuery relies on and is
// itself ASCII-only) rather than full Unicode case-folding.
func firstTermRunePos(content string, terms []string) int {
	lc := asciiLower(content)
	best := -1
	for _, t := range terms {
		if t == "" {
			continue
		}
		idx := strings.Index(lc, asciiLower(t))
		if idx < 0 {
			continue
		}
		if best < 0 || idx < best {
			best = idx
		}
	}
	if best < 0 {
		return -1
	}
	return utf8.RuneCountInString(content[:best])
}

// asciiLower lowercases only ASCII letters (A-Z), leaving every other byte
// untouched. Safe on arbitrary UTF-8: continuation and lead bytes of
// multi-byte sequences are always >= 0x80 so this never rewrites or
// misaligns them, and because ASCII bytes map 1:1 to ASCII bytes, byte
// offsets into the result line up exactly with byte offsets into the input
// (needed by firstTermRunePos's content[:best] slice).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
