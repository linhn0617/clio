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
	if _, err := writeRaw(&buf, msgsWithRaw("A", "A", "B")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "A\nB\n" {
		t.Fatalf("adjacent dup: want %q, got %q", "A\nB\n", got)
	}
}

func TestWriteRawDoesNotOverCollapse(t *testing.T) {
	var buf bytes.Buffer
	if _, err := writeRaw(&buf, msgsWithRaw("A", "B")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "A\nB\n" {
		t.Fatalf("distinct lines: want %q, got %q", "A\nB\n", got)
	}
}

func TestWriteRawAdjacentOnlyNotGlobal(t *testing.T) {
	var buf bytes.Buffer
	if _, err := writeRaw(&buf, msgsWithRaw("A", "B", "A")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "A\nB\nA\n" {
		t.Fatalf("non-adjacent identical must not collapse: want %q, got %q", "A\nB\nA\n", got)
	}
}

func TestWriteRawSkipsPrunedAndReportsIt(t *testing.T) {
	var buf bytes.Buffer
	sawPruned, err := writeRaw(&buf, msgsWithRaw("A", "", "B"))
	if err != nil {
		t.Fatal(err)
	}
	if !sawPruned {
		t.Fatal("sawPruned=false; a blanked raw_json should be reported")
	}
	if got := buf.String(); got != "A\nB\n" {
		t.Fatalf("pruned line not skipped: got %q", got)
	}
}

func TestWriteRawNoPrunedFlagWhenNoneEmpty(t *testing.T) {
	var buf bytes.Buffer
	sawPruned, _ := writeRaw(&buf, msgsWithRaw("A", "B"))
	if sawPruned {
		t.Fatal("sawPruned=true with no empty raw_json")
	}
}

func TestToJSONMessagesNullsPrunedRaw(t *testing.T) {
	jm := toJSONMessages(msgsWithRaw("keep", ""))
	if jm[0].RawJSON == nil || *jm[0].RawJSON != "keep" {
		t.Fatalf("non-pruned raw_json should be present: %+v", jm[0])
	}
	if jm[1].RawJSON != nil {
		t.Fatalf("pruned raw_json must serialize as null, got %v", *jm[1].RawJSON)
	}
	if !anyPruned(msgsWithRaw("keep", "")) {
		t.Fatal("anyPruned should detect the blanked message")
	}
	if anyPruned(msgsWithRaw("a", "b")) {
		t.Fatal("anyPruned false positive")
	}
}
