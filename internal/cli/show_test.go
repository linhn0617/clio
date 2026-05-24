package cli

import (
	"bytes"
	"testing"

	"github.com/linhn0617/clio/internal/sessions"
)

func msgsWithRaw(raws ...string) []sessions.Message {
	out := make([]sessions.Message, 0, len(raws))
	for _, r := range raws {
		out = append(out, sessions.Message{RawJSON: r})
	}
	return out
}

func TestResolveShowFormat(t *testing.T) {
	if got := resolveShowFormat("markdown", true); got != "json" {
		t.Fatalf("jsonFlag should force json, got %q", got)
	}
	if got := resolveShowFormat("raw", false); got != "raw" {
		t.Fatalf("no jsonFlag should keep format, got %q", got)
	}
	if got := resolveShowFormat("", true); got != "json" {
		t.Fatalf("jsonFlag with empty format should be json, got %q", got)
	}
}

func TestWriteRawCollapsesAdjacentDuplicates(t *testing.T) {
	var buf bytes.Buffer
	if err := writeRaw(&buf, msgsWithRaw("A", "A", "B")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "A\nB\n" {
		t.Fatalf("adjacent dup: want %q, got %q", "A\nB\n", got)
	}
}

func TestWriteRawDoesNotOverCollapse(t *testing.T) {
	var buf bytes.Buffer
	if err := writeRaw(&buf, msgsWithRaw("A", "B")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "A\nB\n" {
		t.Fatalf("distinct lines: want %q, got %q", "A\nB\n", got)
	}
}

func TestWriteRawAdjacentOnlyNotGlobal(t *testing.T) {
	var buf bytes.Buffer
	if err := writeRaw(&buf, msgsWithRaw("A", "B", "A")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "A\nB\nA\n" {
		t.Fatalf("non-adjacent identical must not collapse: want %q, got %q", "A\nB\nA\n", got)
	}
}
