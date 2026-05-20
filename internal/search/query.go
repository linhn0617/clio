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
}

// Result is one matched message.
type Result struct {
	MessageID   int64
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

// needsLikeFallback reports whether any term is too short for the trigram
// tokenizer (fewer than 3 runes), which would make FTS MATCH return nothing.
func needsLikeFallback(q string) bool {
	ts := terms(q)
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		if utf8.RuneCountInString(t) < 3 {
			return true
		}
	}
	return false
}
