package search

import (
	"reflect"
	"testing"
)

func TestTerms(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"auth bug", []string{"auth", "bug"}},
		{`"exact phrase" loose`, []string{"exact phrase", "loose"}},
		{"  spaced   out ", []string{"spaced", "out"}},
		{"驗證 流程", []string{"驗證", "流程"}},
	}
	for _, c := range cases {
		if got := terms(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("terms(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestNeedsLikeFallback(t *testing.T) {
	cases := map[string]bool{
		"驗證":       true,  // 2-char CJK
		"資料驗證":     false, // 4-char CJK
		"auth":     false,
		"ab":       true, // 2-char latin
		"auth ab":  true, // one short term
		"資料庫 遷移流程": false,
		"":         false,
	}
	for q, want := range cases {
		if got := needsLikeFallback(q); got != want {
			t.Errorf("needsLikeFallback(%q)=%v want %v", q, got, want)
		}
	}
}
