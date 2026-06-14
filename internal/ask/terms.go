// Package ask builds a retrieval-only evidence bundle answering a natural-language
// question from indexed history. It performs no text generation and no network
// call: the cited excerpts are returned for the caller to synthesize from.
package ask

import "strings"

// cjkGram is the CJK expansion width; it matches the FTS trigram tokenizer so an
// unspaced Chinese question still hits the index. It is also coupled to
// search.partitionTerms' >=3-rune long/short split: trigrams reach the FTS (long)
// tier and bigrams the LIKE (short) tier — changing that threshold would silently
// shift which CJK grams are searchable.
const cjkGram = 3

// maxTerms caps the extracted term count. A long pasted question (especially an
// unspaced CJK paragraph, which expands into one gram per character) would
// otherwise build an FTS OR expression past SQLite's hard depth limit (1000) and
// a wide per-row LIKE scan; the first maxTerms grams are a sufficient basis.
const maxTerms = 64

// extractTerms reduces a natural-language question to the content terms used for
// retrieval: lowercased, surrounding punctuation trimmed, stopwords removed, and
// de-duplicated in first-seen order. Unspaced CJK runs are expanded into
// overlapping trigrams (Chinese is written without spaces, so a whole sentence is
// one whitespace token; matching it as one phrase would only find near-exact
// substrings). When the question is nothing but stopwords it falls back to all
// terms, so retrieval is never run on an empty set.
func extractTerms(question string) []string {
	raw := splitTerms(question)
	content := make([]string, 0, len(raw))
	for _, t := range raw {
		if !stopwords[t] {
			content = append(content, expand(t, false)...)
		}
	}
	if len(content) == 0 {
		// All-stopword question: fall back to the raw tokens, grammed without
		// stopword removal, so retrieval is never run on an empty set (true for CJK
		// too — re-stripping stopwords here would leave nothing).
		for _, t := range raw {
			content = append(content, expand(t, true)...)
		}
	}
	out := dedupe(content)
	if len(out) > maxTerms {
		out = out[:maxTerms]
	}
	return out
}

// expand passes a non-CJK token through unchanged, and splits a CJK-bearing token
// into its CJK and non-CJK runs: a CJK run becomes overlapping trigrams (so a
// partial mention still matches the trigram index) plus bigrams (so a 2-rune
// keyword reaches the LIKE fallback); a non-CJK run is trimmed and kept. In
// content mode a CJK run is first split on its stopwords (我們 / 怎麼) and non-CJK
// stopword segments are dropped; in raw mode (the all-stopword fallback) nothing
// is stripped. A lone CJK char is dropped — a 1-rune LIKE matches almost anything.
func expand(t string, raw bool) []string {
	if !hasCJK(t) {
		return []string{t}
	}
	var out []string
	runes := []rune(t)
	for i := 0; i < len(runes); {
		if isCJK(runes[i]) {
			j := i
			for j < len(runes) && isCJK(runes[j]) {
				j++
			}
			run := runes[i:j]
			segs := [][]rune{run}
			if !raw {
				segs = segmentCJK(run)
			}
			for _, seg := range segs {
				// A lone CJK rune is never emitted (even in raw fallback): a 1-rune
				// substring LIKE matches almost the whole index, which is noise, not
				// signal — an empty result is more honest than a grab-bag.
				out = append(out, cjkGrams(seg, cjkGram)...)
				out = append(out, cjkGrams(seg, 2)...)
			}
			i = j
			continue
		}
		j := i
		for j < len(runes) && !isCJK(runes[j]) {
			j++
		}
		if seg := strings.Trim(string(runes[i:j]), punctCutset); seg != "" && (raw || !stopwords[seg]) {
			out = append(out, seg)
		}
		i = j
	}
	return out
}

// cjkStopwords is the multi-rune CJK subset of stopwords, used to split unspaced
// CJK runs into content segments. Single-rune stopwords (在 / 有 / 是) are excluded:
// they are too often mid-word to split on safely.
var cjkStopwords = func() map[string]bool {
	m := map[string]bool{}
	for w := range stopwords {
		if r := []rune(w); len(r) >= 2 && isCJK(r[0]) {
			m[w] = true
		}
	}
	return m
}()

// segmentCJK splits a CJK run into content segments, dropping any CJK stopword
// (longest match wins) as a delimiter.
func segmentCJK(run []rune) [][]rune {
	var segs [][]rune
	var cur []rune
	for i := 0; i < len(run); {
		if w := cjkStopwordPrefix(run[i:]); w > 0 {
			if len(cur) > 0 {
				segs = append(segs, cur)
				cur = nil
			}
			i += w
			continue
		}
		cur = append(cur, run[i])
		i++
	}
	if len(cur) > 0 {
		segs = append(segs, cur)
	}
	return segs
}

// cjkStopwordPrefix returns the rune length of the longest CJK stopword that is a
// prefix of s, or 0.
func cjkStopwordPrefix(s []rune) int {
	best := 0
	for sw := range cjkStopwords {
		r := []rune(sw)
		if len(r) > best && len(r) <= len(s) && string(s[:len(r)]) == sw {
			best = len(r)
		}
	}
	return best
}

// cjkGrams returns the overlapping n-grams of run, or nil when run is shorter
// than n.
func cjkGrams(run []rune, n int) []string {
	if len(run) < n {
		return nil
	}
	out := make([]string, 0, len(run)-n+1)
	for k := 0; k+n <= len(run); k++ {
		out = append(out, string(run[k:k+n]))
	}
	return out
}

// isCJK reports whether r is a CJK ideograph (Unified + Extension A), the scripts
// clio's index is exercised with. Kana/Hangul are out of scope for v1.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

func hasCJK(s string) bool {
	for _, r := range s {
		if isCJK(r) {
			return true
		}
	}
	return false
}

// punctCutset is trimmed from the ends of each token (ASCII + common full-width
// CJK punctuation), so "bug?" and "遷移。" reduce to "bug" and "遷移".
const punctCutset = ".,?!;:\"'()[]{}…？。，！；：、「」『』（）"

// splitTerms lowercases the question, splits on whitespace, and trims surrounding
// punctuation from each token, dropping empties.
func splitTerms(question string) []string {
	fields := strings.Fields(strings.ToLower(question))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.Trim(f, punctCutset); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// stopwords are dropped from a question before retrieval. English question and
// function words, plus common space-separated Chinese particles, pronouns, and
// question words.
var stopwords = map[string]bool{
	"how": true, "what": true, "when": true, "where": true, "why": true,
	"who": true, "which": true, "whom": true, "whose": true,
	"did": true, "do": true, "does": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "am": true,
	"the": true, "a": true, "an": true, "to": true, "of": true, "in": true,
	"on": true, "at": true, "for": true, "and": true, "or": true, "but": true,
	"we": true, "i": true, "you": true, "it": true, "they": true, "he": true,
	"she": true, "that": true, "this": true, "these": true, "those": true,
	"my": true, "our": true, "your": true, "with": true, "about": true,
	"from": true, "by": true, "as": true, "can": true, "could": true,
	"would": true, "should": true, "will": true, "shall": true, "me": true,
	"us": true, "there": true, "here": true, "any": true, "some": true,
	"的": true, "了": true, "嗎": true, "呢": true, "吧": true, "啊": true,
	"我": true, "我們": true, "你": true, "你們": true, "他": true, "她": true,
	"怎麼": true, "怎樣": true, "如何": true, "什麼": true, "為什麼": true,
	"那個": true, "這個": true, "那": true, "這": true, "是": true, "在": true,
	"有": true, "跟": true, "和": true, "與": true,
}
