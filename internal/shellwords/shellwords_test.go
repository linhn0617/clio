package shellwords

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		ok   bool
	}{
		{`/a/clio recall`, []string{"/a/clio", "recall"}, true},
		{`'/a b/clio' recall`, []string{"/a b/clio", "recall"}, true},
		{`"/a b/clio" file-history --hook`, []string{"/a b/clio", "file-history", "--hook"}, true},
		{`/a\ b/clio recall`, []string{"/a b/clio", "recall"}, true},
		{`clio recall;`, []string{"clio", "recall;"}, true}, // operators are NOT split: recognisers rely on exact shape
		{`'unterminated recall`, nil, false},
		{`trailing\`, nil, false},
		{``, nil, true},
	}
	for _, c := range cases {
		got, ok := Split(c.in)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Split(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestScan(t *testing.T) {
	w := func(s string) Token { return Token{Text: s} }
	o := func(s string) Token { return Token{Text: s, Op: true} }
	cases := []struct {
		in   string
		want []Token
		ok   bool
	}{
		{`cat a;cat b`, []Token{w("cat"), w("a"), o(";"), w("cat"), w("b")}, true},
		{`(cat a)|wc`, []Token{o("("), w("cat"), w("a"), o(")|"), w("wc")}, true},
		{`cat a>>out`, []Token{w("cat"), w("a"), o(">>"), w("out")}, true},
		{`cat <<-EOF`, []Token{w("cat"), o("<<"), w("-EOF")}, true},
		{`cat "a b;c"`, []Token{w("cat"), w("a b;c")}, true},
		{`cat 'a|b'x`, []Token{w("cat"), w("a|bx")}, true},
		{`cat a\;b`, []Token{w("cat"), w("a;b")}, true},
		{`cat \ a`, []Token{w("cat"), w(" a")}, true},
		{"cat a # note b.go\ncat c", []Token{w("cat"), w("a"), o("\n"), w("cat"), w("c")}, true},
		{`echo a#b`, []Token{w("echo"), w("a#b")}, true},
		{`# only comment`, nil, true},
		{`cat 'oops`, nil, false},
		{`cat "oops`, nil, false},
		{"cat a\ncat b", []Token{w("cat"), w("a"), o("\n"), w("cat"), w("b")}, true},
	}
	for _, c := range cases {
		got, ok := Scan(c.in)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Scan(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
