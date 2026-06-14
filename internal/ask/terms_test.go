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
