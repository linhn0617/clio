package ingest

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/model"
)

// fakeReplaySource is a minimal whole-file-replay Source used to prove
// IngestFile (task 2.2) forces startOffset=0/startSeq=0/changeFull for a
// source that declares WholeFileReplay()==true, regardless of the stored
// byte offset. Its format mirrors just enough of Gemini's op-log shape to
// exercise the property that matters: a plain line APPENDS one message, but
// a line "SET:a,b,c" REPLACES the whole reconstructed message list with
// a,b,c (like a Gemini `$set`). Crucially a `SET:` line can be a true
// byte-level append (the file's existing prefix bytes never change), so the
// existing fingerprint-based rewrite detector (incremental.go) would treat
// it as a safe incremental append and resume from the stored offset — WRONG
// for this format, since the appended line means "discard everything
// before". Only a source-level whole-file-replay force (task 2.2) parses
// this correctly. This is deliberately not representative of Gemini's real
// $set JSON format (that is exercised once the gemini adapter exists); this
// fixture only proves the ingest.go orchestrator wiring.
type fakeReplaySource struct{ root string }

func (fakeReplaySource) Name() string { return "fakereplay" }
func (s fakeReplaySource) Owns(path string) bool {
	return strings.HasSuffix(path, ".fakereplay.jsonl")
}
func (fakeReplaySource) SessionIDFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".fakereplay.jsonl")
}
func (s fakeReplaySource) Roots() ([]string, error) { return []string{s.root}, nil }
func (fakeReplaySource) WholeFileReplay() bool      { return true }

// ParseFile asserts startOffset/startSeq arrived as 0 — the orchestrator
// must never pass anything else to a whole-file-replay source — then
// replays every line in the file per the SET:/append rules above.
func (s fakeReplaySource) ParseFile(ing *Ingester, f *os.File, startOffset int64, startSeq int, path string) (parseResult, error) {
	if startOffset != 0 {
		panic("fakeReplaySource.ParseFile got non-zero startOffset")
	}
	if startSeq != 0 {
		panic("fakeReplaySource.ParseFile got non-zero startSeq")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return parseResult{}, err
	}
	uuid := s.SessionIDFromPath(path)
	var texts []string // reconstructed message list, replayed in file order
	var consumed int64
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			consumed += int64(len(line))
			text := strings.TrimSpace(line)
			if rest, ok := strings.CutPrefix(text, "SET:"); ok {
				texts = strings.Split(rest, ",") // full overwrite, last writer wins
			} else if strings.HasPrefix(text, "#") {
				// comment/padding line: counted in consumed bytes, never a message.
				// Lets fixtures pad past fingerprintWindow (512B, incremental.go)
				// without polluting the asserted message contents.
			} else if text != "" {
				texts = append(texts, text) // bare append
			}
		}
		if err != nil {
			break // io.EOF or other: whole file is always fully consumed by replay
		}
	}
	var msgs []model.Message
	for i, text := range texts {
		msgs = append(msgs, model.Message{SessionUUID: uuid, Seq: i, Role: model.RoleUser, Content: text})
	}
	sess := model.Session{UUID: uuid, SourceFile: path, Source: "fakereplay"}
	return parseResult{Session: sess, Messages: msgs, Consumed: consumed}, nil
}

// fakeReplayPadding is a comment line whose length pushes a fixture's initial
// size past fingerprintWindow (512B, incremental.go). Below that window, the
// stored head-fingerprint's covered byte range itself grows with the file
// (headFingerprint hashes only min(size, window) bytes), so ANY append to a
// small file changes headFP and trips changeFull via classifyChange's
// existing safety net — independent of whole-file-replay. Padding past the
// window keeps these fixtures's later "true append" scenarios from
// accidentally passing for that unrelated reason.
var fakeReplayPadding = "#" + strings.Repeat("p", 600) + "\n"

func writeFakeReplayFile(t *testing.T, dir, uuid, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, uuid+".fakereplay.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestWholeFileReplaySourceReingestsWholeFileOnChange is the spec scenario
// "A whole-file-replay source re-ingests its whole file on change". The
// second write is a TRUE byte-level append (the existing prefix bytes are
// untouched) of a "SET:" line that semantically discards everything before
// it — exactly the Gemini `$set` shape (design.md §4). A byte-fingerprint
// incremental resume would see an unchanged prefix, treat this as a safe
// append, and parse only the new tail at a fresh seq — duplicating the
// earlier messages instead of replacing them. Only forcing
// startOffset=0/startSeq=0/changeFull for a whole-file-replay source
// (task 2.2) gets this right; fakeReplaySource.ParseFile also panics outright
// if it ever receives a non-zero startOffset/startSeq, so an un-fixed
// orchestrator fails this test loudly rather than silently.
func TestWholeFileReplaySourceReingestsWholeFileOnChange(t *testing.T) {
	root := t.TempDir()
	path := writeFakeReplayFile(t, root, "wf-1", fakeReplayPadding+"line one\nline two\n")

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(fakeReplaySource{root: root})
	emptyCC := t.TempDir()

	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}
	assertFakeReplayContents(t, database, []string{"line one", "line two"})

	// True append: open O_APPEND, write only new bytes. The existing "line
	// one\nline two\n" prefix is never rewritten on disk.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("SET:x,y,z\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)

	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}
	// The $set discards "line one"/"line two" entirely: only x,y,z survive,
	// with no stale rows left behind from the pre-$set state.
	assertFakeReplayContents(t, database, []string{"x", "y", "z"})
}

// TestWholeFileReplaySourceSkipsUnchangedFile proves the stored byte offset
// is used only as a change-detector: an unchanged file must not be reparsed
// (fakeReplaySource.ParseFile would panic on a non-zero startOffset, so a
// spurious incremental-resume attempt would fail loudly; here we just assert
// no reparse happens via Stats).
func TestWholeFileReplaySourceSkipsUnchangedFile(t *testing.T) {
	root := t.TempDir()
	writeFakeReplayFile(t, root, "wf-2", "only line\n")

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(fakeReplaySource{root: root})
	emptyCC := t.TempDir()

	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}
	st, err := ing.IngestAll(context.Background(), emptyCC, false) // unchanged
	if err != nil {
		t.Fatal(err)
	}
	if st.FilesIngested != 0 || st.FilesSkipped != 1 {
		t.Fatalf("expected the unchanged file to be skipped, got %+v", st)
	}
}

// TestWholeFileReplayIdempotentOnRepeatedIngest is the spec's idempotency
// crux (design.md §4 / tasks.md §6.3, exercised here at the orchestrator
// wiring level): ingest, re-ingest unchanged (no-op), then re-ingest after an
// edit — the final rows must equal a from-scratch ingest of the final bytes
// (no duplicate rows, no stale rows).
func TestWholeFileReplayIdempotentOnRepeatedIngest(t *testing.T) {
	root := t.TempDir()
	path := writeFakeReplayFile(t, root, "wf-3", fakeReplayPadding+"a\nb\n")

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(fakeReplaySource{root: root})
	emptyCC := t.TempDir()

	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil { // unchanged: no-op
		t.Fatal(err)
	}
	// True append of a $set-style line that edits/replaces the earlier state.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("SET:a EDITED,b,c\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)
	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}

	// From-scratch comparison: a brand new DB ingesting only the final bytes.
	freshRoot := t.TempDir()
	writeFakeReplayFile(t, freshRoot, "wf-3", "a\nb\nSET:a EDITED,b,c\n")
	freshDB := openTestDB(t)
	freshIng := New(freshDB, nil)
	freshIng.AddSource(fakeReplaySource{root: freshRoot})
	if _, err := freshIng.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}

	got := fakeReplayContents(t, database)
	want := fakeReplayContents(t, freshDB)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("repeated-ingest rows = %v, want (from-scratch) %v", got, want)
	}
}

// fakeReplayContents returns wf-*'s indexed message contents, ordered by seq.
func fakeReplayContents(t *testing.T, database *db.DB) []string {
	t.Helper()
	rows, err := database.Query(`SELECT content FROM messages WHERE session_uuid LIKE 'wf-%' ORDER BY session_uuid, seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func assertFakeReplayContents(t *testing.T, database *db.DB, want []string) {
	t.Helper()
	got := fakeReplayContents(t, database)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fakereplay contents = %v, want %v", got, want)
	}
}
