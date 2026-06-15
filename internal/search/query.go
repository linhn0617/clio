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
}

// Result is one matched message.
type Result struct {
	MessageID   int64
	Seq         int
	SessionUUID string
	ProjectPath string
	Role        string
	TS          int64
	Snippet     string
	Score       float64
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
