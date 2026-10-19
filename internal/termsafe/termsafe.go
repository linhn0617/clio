// Package termsafe removes terminal control sequences from text bound for a
// human's terminal. Indexed transcripts can carry anything their authors (or
// pasted web pages, tool output) put there, so an unfiltered print would let
// them write the clipboard (OSC 52), retitle the window, or clear the screen.
package termsafe

import (
	"strings"
	"unicode/utf8"
)

// Strip drops ESC sequences (CSI, OSC, DCS/APC/PM/SOS, and two-byte forms),
// C0 controls other than \n and \t, DEL, and C1 controls. A C1 control is
// recognised both UTF-8 encoded (U+0080–U+009F) and as a raw byte 0x80–0x9F
// outside any valid UTF-8 sequence, and the C1 introducers (CSI, OSC, DCS, SOS,
// PM, APC) and ST act like their ESC forms: some terminals honour them. \r goes
// too: it would let a line overwrite the one before it on screen. An
// unterminated sequence is dropped through the end of the string rather than
// guessed at, unless a new ESC appears first — that aborts the earlier sequence
// and parsing starts afresh there, as a terminal does. Plain text, CJK and emoji
// pass through byte for byte.
func Strip(s string) string {
	if !needsStrip(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = skipEscape(s, i)
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < 0x80:
			b.WriteByte(c)
			i++
		default:
			if code, n := c1At(s, i); n > 0 {
				i = skipC1(s, code, i+n)
				continue
			}
			// Valid UTF-8 is copied whole; an invalid byte outside the C1 range
			// is not a control, so it is kept as-is.
			_, n := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+n])
			i += n
		}
	}
	return b.String()
}

// needsStrip is the fast path: most strings are clean.
func needsStrip(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x80 {
			if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
				return true
			}
			i++
			continue
		}
		if _, n := c1At(s, i); n > 0 {
			return true
		}
		_, n := utf8.DecodeRuneInString(s[i:])
		i += n
	}
	return false
}

// c1At reports the C1 control (0x80–0x9F) starting at s[i] and its width in
// bytes: 2 for the UTF-8 encoding, 1 for a raw byte that is not part of a valid
// UTF-8 sequence. n is 0 when s[i] does not start a C1 control. s[i] must start
// a rune: a UTF-8 continuation byte read on its own would look like a raw C1.
func c1At(s string, i int) (code byte, n int) {
	r, w := utf8.DecodeRuneInString(s[i:])
	switch {
	case r == utf8.RuneError && w == 1 && s[i] >= 0x80 && s[i] <= 0x9f:
		return s[i], 1
	case r >= 0x80 && r <= 0x9f:
		return byte(r), w
	}
	return 0, 0
}

// skipC1 returns the index just past the sequence a C1 control introduces;
// i is the index just after the control itself.
func skipC1(s string, code byte, i int) int {
	switch code {
	case 0x9b: // CSI
		return skipCSI(s, i)
	case 0x9d: // OSC
		return skipString(s, i, true)
	case 0x90, 0x98, 0x9e, 0x9f: // DCS, SOS, PM, APC
		return skipString(s, i, false)
	}
	return i
}

// skipEscape returns the index just past the sequence starting at s[i] == ESC.
func skipEscape(s string, i int) int {
	i++
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']': // OSC: ends at BEL or ST
		return skipString(s, i+1, true)
	case 'P', '_', '^', 'X': // DCS, APC, PM, SOS: end at ST only
		return skipString(s, i+1, false)
	default: // two-char sequence; step a whole rune so UTF-8 is never split
		_, n := utf8.DecodeRuneInString(s[i:])
		return i + n
	}
}

// skipCSI skips CSI parameters/intermediates and one final byte 0x40–0x7E.
func skipCSI(s string, i int) int {
	for i < len(s) {
		c := s[i]
		if c == 0x1b {
			break // a new ESC aborts this one; leave it to be parsed afresh
		}
		i++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return i
}

// skipString skips a control string up to ST (ESC \ or C1 ST) or, for OSC,
// BEL. It steps by rune so a UTF-8 continuation byte in the 0x80–0x9F range
// (e.g. inside 东) is not mistaken for a raw ST.
func skipString(s string, i int, belEnds bool) int {
	for i < len(s) {
		if belEnds && s[i] == 0x07 {
			return i + 1
		}
		if s[i] == 0x1b {
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			return i // an ESC that is not ST aborts the string
		}
		if s[i] >= 0x80 {
			if code, n := c1At(s, i); n > 0 && code == 0x9c {
				return i + n
			}
			_, n := utf8.DecodeRuneInString(s[i:])
			i += n
			continue
		}
		i++
	}
	return i
}

// OneLine renders indexed text as one trimmed line for a terminal list: escapes
// stripped, newlines turned into spaces, cut to max runes with a trailing "…".
func OneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(Strip(s)), "\n", " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
