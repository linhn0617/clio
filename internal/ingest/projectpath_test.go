package ingest

import "testing"

func TestFallbackProjectPath(t *testing.T) {
	cases := map[string]string{
		"-Users-lin-Herd-foo": "/Users/lin/Herd/foo",
		"-Users-lin":          "/Users/lin",
		"":                    "",
	}
	for in, want := range cases {
		if got := fallbackProjectPath(in); got != want {
			t.Errorf("fallbackProjectPath(%q)=%q want %q", in, got, want)
		}
	}
}
