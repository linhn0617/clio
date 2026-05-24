package search

import (
	"reflect"
	"testing"
)

func TestBuildMatchQuery(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"c++", "foo"}, `"c++" "foo"`},
		{[]string{`has"quote`}, `"has""quote"`},
		{[]string{}, ""},
		{[]string{"auth"}, `"auth"`},
		{[]string{"auth", "ui"}, `"auth" "ui"`},
	}
	for _, c := range cases {
		got := buildMatchQuery(c.in)
		if got != c.want {
			t.Errorf("buildMatchQuery(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

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

func TestPartitionTerms(t *testing.T) {
	cases := []struct {
		in        []string
		wantLong  []string
		wantShort []string
	}{
		{[]string{"auth", "ui"}, []string{"auth"}, []string{"ui"}},
		{[]string{"auth", "flow"}, []string{"auth", "flow"}, nil},
		{[]string{"ui", "ok"}, nil, []string{"ui", "ok"}},
		{[]string{}, nil, nil},
		{[]string{"資料庫"}, []string{"資料庫"}, nil}, // 3-char CJK is long
		{[]string{"驗證"}, nil, []string{"驗證"}},   // 2-char CJK is short
	}
	for _, c := range cases {
		gotLong, gotShort := partitionTerms(c.in)
		if !reflect.DeepEqual(gotLong, c.wantLong) || !reflect.DeepEqual(gotShort, c.wantShort) {
			t.Errorf("partitionTerms(%v) = (%v, %v), want (%v, %v)",
				c.in, gotLong, gotShort, c.wantLong, c.wantShort)
		}
	}
}
