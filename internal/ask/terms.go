// Package ask builds a retrieval-only evidence bundle answering a natural-language
// question from indexed history. It performs no text generation and no network
// call: the cited excerpts are returned for the caller to synthesize from.
package ask

import "strings"

// cjkGram is the CJK expansion width; it matches the FTS trigram tokenizer so an
// unspaced Chinese question still hits the index.
const cjkGram = 3

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
			content = append(content, expandTerm(t)...)
		}
	}
	if len(content) == 0 {
		for _, t := range raw {
			content = append(content, expandTerm(t)...)
		}
	}
	return dedupe(content)
}

// expandTerm passes a non-CJK token through unchanged, but splits a token that
// contains CJK into its CJK and non-CJK runs: a CJK run of >= cjkGram runes
// becomes overlapping cjkGram-grams (so partial mentions still match the trigram
// index), a shorter CJK run is kept whole (the LIKE fallback handles it), and a
// non-CJK run is trimmed and kept unless it is a stopword.
func expandTerm(t string) []string {
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
			if len(run) >= cjkGram {
				for k := 0; k+cjkGram <= len(run); k++ {
					out = append(out, string(run[k:k+cjkGram]))
				}
			} else {
				out = append(out, string(run))
			}
			i = j
			continue
		}
		j := i
		for j < len(runes) && !isCJK(runes[j]) {
			j++
		}
		if seg := strings.Trim(string(runes[i:j]), punctCutset); seg != "" && !stopwords[seg] {
			out = append(out, seg)
		}
		i = j
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
