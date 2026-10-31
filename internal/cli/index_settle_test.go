package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/ingest"
)

// The summary names the files a full reindex gave up on, terminal-safe, with a
// hint, on stderr; stdout keeps the counts line alone.
func TestPrintIndexSummaryNamesStillChangingFiles(t *testing.T) {
	var out, errOut bytes.Buffer
	printIndexSummary(&out, &errOut, ingest.Stats{FilesScanned: 3, FilesIngested: 1, FilesSkipped: 2,
		StillChanging: []string{"/p/a.jsonl", "/p/b\x1b[2J.jsonl"}})
	if got := out.String(); got != "indexed: 3 files (1 ingested, 2 skipped), 0 messages added\n" {
		t.Errorf("stdout = %q", got)
	}
	e := errOut.String()
	for _, want := range []string{"2 file(s)", "/p/a.jsonl", "/p/b.jsonl", "clio index --full", "Claude Code is closed"} {
		if !strings.Contains(e, want) {
			t.Errorf("stderr lacks %q:\n%s", want, e)
		}
	}
	if strings.Contains(e, "\x1b") {
		t.Errorf("escape sequence reached the terminal: %q", e)
	}

	out.Reset()
	errOut.Reset()
	printIndexSummary(&out, &errOut, ingest.Stats{FilesScanned: 1, FilesIngested: 1})
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q with nothing still changing", errOut.String())
	}
}

// `clio index --full` prints its summary through printIndexSummary.
func TestIndexFullPrintsSummary(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-1", 1)
	database.Close()
	out, err := runIndexCmd(t, "--full")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "indexed: 1 files (1 ingested, 0 skipped)") {
		t.Errorf("stdout = %q, want the summary line", out)
	}
}

// A pruned session whose file a full reindex skipped as still changing is not
// an unrestorable residual: the summary already names the file, and the next
// reindex restores it. Any other pruned session still fails the check.
func TestCheckPrunedResidualsSkipsStillChangingFiles(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")

	var errOut bytes.Buffer
	if err := checkPrunedResiduals(context.Background(), database, &errOut, []string{path}); err != nil {
		t.Errorf("err = %v for a session whose file is still changing\n%s", err, errOut.String())
	}
	if strings.Contains(errOut.String(), "sess-old") {
		t.Errorf("stderr reports the still-changing session as unrestorable:\n%s", errOut.String())
	}

	errOut.Reset()
	err := checkPrunedResiduals(context.Background(), database, &errOut, []string{"/elsewhere/other.jsonl"})
	if err == nil || !strings.Contains(err.Error(), "could not be restored") || !strings.Contains(errOut.String(), "sess-old") {
		t.Errorf("err = %v, stderr = %q; want the residual reported", err, errOut.String())
	}
}
