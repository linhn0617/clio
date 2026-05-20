package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyChange(t *testing.T) {
	prior := &FileState{LastSize: 100, LastMTime: 1000}
	cases := []struct {
		name        string
		prior       *FileState
		size, mtime int64
		want        changeKind
	}{
		{"never seen", nil, 50, 1, changeFull},
		{"unchanged", prior, 100, 1000, changeSkip},
		{"grew", prior, 200, 1001, changeIncremental},
		{"shrank", prior, 50, 1001, changeFull},
		{"same size new mtime", prior, 100, 1001, changeIncremental},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyChange(c.prior, c.size, c.mtime); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestLastCompleteNewline(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"a\nb\n", 4},
		{"a\nb", 2},      // partial trailing line
		{"nonewline", 0}, // no complete line
		{"", 0},
		{"\n", 1},
	}
	for _, c := range cases {
		if got := lastCompleteNewline([]byte(c.in)); got != c.want {
			t.Errorf("lastCompleteNewline(%q)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestFingerprintAt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("hello world this is content"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	fp1, err := fingerprintAt(f, 11)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	// Same offset, same bytes -> same fingerprint (stable).
	fp2, _ := fingerprintAt(f, 11)
	if fp1 != fp2 {
		t.Fatal("fingerprint not stable")
	}
	// offset 0 -> empty.
	if fp, _ := fingerprintAt(f, 0); fp != "" {
		t.Fatal("expected empty fingerprint at offset 0")
	}
}
