package ask

import (
	"slices"
	"strings"
	"testing"
)

// A very long (e.g. pasted) question must not produce an unbounded term set: the
// FTS OR expression has a hard depth limit, so extractTerms caps the term count.
func TestExtractTermsCapsCount(t *testing.T) {
	long := strings.Repeat("資料庫遷移驗證流程設計效能調校", 80) // long unspaced CJK paste
	if got := extractTerms(long); len(got) > maxTerms {
		t.Fatalf("expected at most %d terms, got %d", maxTerms, len(got))
	}
}

func TestExtractTermsStripsStopwords(t *testing.T) {
	// Question words and function words drop out; content terms survive
	// (lowercased, trailing punctuation trimmed).
	got := extractTerms("How did we fix the auth bug?")
	want := []string{"fix", "auth", "bug"}
	if !slices.Equal(got, want) {
		t.Fatalf("extractTerms = %v, want %v", got, want)
	}
}

func TestExtractTermsFallbackWhenAllStopwords(t *testing.T) {
	// A question that is nothing but stopwords must not retrieve on an empty
	// term set; fall back to every term.
	got := extractTerms("how did we")
	want := []string{"how", "did", "we"}
	if !slices.Equal(got, want) {
		t.Fatalf("extractTerms = %v, want %v", got, want)
	}
}

func TestExtractTermsDedupes(t *testing.T) {
	got := extractTerms("auth AUTH bug")
	want := []string{"auth", "bug"}
	if !slices.Equal(got, want) {
		t.Fatalf("extractTerms = %v, want %v", got, want)
	}
}

// An unspaced CJK question must not collapse into one near-exact phrase: long CJK
// runs expand into overlapping trigrams (matching the FTS trigram index), so a
// session that mentions only part of the phrase is still retrievable.
func TestExtractTermsExpandsUnspacedCJK(t *testing.T) {
	got := extractTerms("資料庫遷移")
	if slices.Contains(got, "資料庫遷移") {
		t.Fatalf("CJK run must be split into trigrams, not kept whole: %v", got)
	}
	for _, want := range []string{"資料庫", "料庫遷", "庫遷移"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing trigram %q in %v", want, got)
		}
	}
}

// Stopwords mixed into an unspaced CJK question shouldn't block the content
// trigrams: a partial mention (身份驗證) is still reachable from the question.
func TestExtractTermsCJKQuestionReachesContent(t *testing.T) {
	got := extractTerms("我們怎麼修復身份驗證問題")
	if !slices.Contains(got, "身份驗") || !slices.Contains(got, "份驗證") {
		t.Fatalf("expected content trigrams of 身份驗證 in %v", got)
	}
}

// A 2-rune CJK keyword embedded in a longer unspaced run must also be emitted as a
// bigram, so it reaches the LIKE fallback (trigrams alone miss it when a session
// uses the word without the question's exact 3-char boundary).
func TestExtractTermsEmitsCJKBigrams(t *testing.T) {
	got := extractTerms("我們怎麼修復驗證")
	if !slices.Contains(got, "驗證") {
		t.Fatalf("expected bigram 驗證 (LIKE-reachable) in %v", got)
	}
}

// CJK runs are split on stopwords before gramming, so common question words like
// 我們 / 怎麼 don't leak into the gram set and crowd out the real keyword.
func TestExtractTermsDropsCJKStopwordGrams(t *testing.T) {
	got := extractTerms("我們怎麼修復驗證")
	for _, bad := range []string{"我們", "怎麼", "我們怎", "們怎麼", "怎麼修", "麼修復"} {
		if slices.Contains(got, bad) {
			t.Fatalf("stopword-derived gram %q should not be emitted: %v", bad, got)
		}
	}
	if !slices.Contains(got, "驗證") || !slices.Contains(got, "修復驗") {
		t.Fatalf("content grams missing after stopword split: %v", got)
	}
}

// Only multi-rune stopwords delimit a CJK run. A single-rune stopword (在/有/是)
// is often mid-word, so splitting on it would drop grams spanning that character
// (在線驗證 must still produce 在線 / 在線驗).
func TestExtractTermsKeepsSingleRuneStopwordInWord(t *testing.T) {
	got := extractTerms("在線驗證")
	if !slices.Contains(got, "在線") || !slices.Contains(got, "在線驗") {
		t.Fatalf("grams spanning 在 should survive (在 must not split the run): %v", got)
	}
}

// The all-stopword fallback must reach retrieval for CJK too: a pure question-word
// prompt still yields searchable grams rather than an empty term set.
func TestExtractTermsCJKAllStopwordFallback(t *testing.T) {
	if got := extractTerms("我們怎麼"); len(got) == 0 {
		t.Fatalf("all-stopword CJK question must fall back to non-empty terms, got %v", got)
	}
	if got := extractTerms("我們 怎麼"); len(got) == 0 {
		t.Fatalf("spaced all-stopword CJK question must fall back to non-empty terms, got %v", got)
	}
}

// A question of only single-rune CJK runes yields no term: a lone common rune
// (在/了/是) as a substring LIKE matches almost the whole index, so emitting it
// would return a garbage grab-bag — worse than a clean empty result.
func TestExtractTermsDropsLoneCJKRune(t *testing.T) {
	if got := extractTerms("在"); len(got) != 0 {
		t.Fatalf("a lone CJK rune must not produce a term, got %v", got)
	}
	if got := extractTerms("我 嗎"); len(got) != 0 {
		t.Fatalf("only single-rune CJK stopwords must yield no term, got %v", got)
	}
}
