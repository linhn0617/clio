package ask

import (
	"slices"
	"testing"
)

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
