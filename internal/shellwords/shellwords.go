// Package shellwords is the one minimal POSIX-ish word splitter shared by the
// hook-command recognisers (claudeconfig) and the Bash file-context hook (cli).
// It is a lexical filter, not a shell: no expansion, no heredoc awareness.
package shellwords

import "strings"

// Token is one lexical unit: a word, or a run of shell operator characters
// (a newline is always its own operator token).
type Token struct {
	Text string
	Op   bool
}

const operators = ";|&()<>"

// Split is the recogniser form: whitespace-separated words, '…' literal, "…"
// with backslash escapes, and a bare \x escape (which is what ShellQuote emits
// between '…' runs). Operators are NOT split — `clio recall;` stays two words,
// so a hook command with trailing shell syntax is never taken for clio's.
// ok is false for an unterminated quote or trailing backslash, which no shell
// would accept either.
func Split(cmd string) (words []string, ok bool) {
	toks, ok := scan(cmd, false)
	for _, t := range toks {
		words = append(words, t.Text)
	}
	return words, ok
}

// Scan is Split plus shell structure: operator runs (`;|&()<>`) split words and
// come back as Op tokens, a newline is an Op token of its own, and an unquoted
// `#` at the start of a word comments out the rest of the line.
func Scan(cmd string) ([]Token, bool) { return scan(cmd, true) }

func scan(cmd string, shell bool) (toks []Token, ok bool) {
	var cur, op strings.Builder
	inWord := false
	flushWord := func() {
		if inWord {
			toks = append(toks, Token{Text: cur.String()})
			cur.Reset()
			inWord = false
		}
	}
	flushOp := func() {
		if op.Len() > 0 {
			toks = append(toks, Token{Text: op.String(), Op: true})
			op.Reset()
		}
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if shell && !strings.ContainsRune(operators, c) {
			flushOp()
		}
		switch {
		case c == '\n' && shell:
			flushWord()
			toks = append(toks, Token{Text: "\n", Op: true})
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flushWord()
		case shell && strings.ContainsRune(operators, c):
			flushWord()
			op.WriteRune(c)
		case shell && c == '#' && !inWord:
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case c == '\'':
			inWord = true
			i++
			for ; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
			if i == len(rs) {
				return nil, false
			}
		case c == '"':
			inWord = true
			i++
			for ; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[i+1]) {
					i++
				}
				cur.WriteRune(rs[i])
			}
			if i == len(rs) {
				return nil, false
			}
		case c == '\\':
			if i+1 == len(rs) {
				return nil, false
			}
			inWord = true
			i++
			cur.WriteRune(rs[i])
		default:
			inWord = true
			cur.WriteRune(c)
		}
	}
	flushWord()
	flushOp()
	return toks, true
}
