package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

func putRetention(t *testing.T, d *db.DB, v string) {
	t.Helper()
	if err := d.PutSetting(context.Background(), db.RawRetentionKey, v); err != nil {
		t.Fatal(err)
	}
}

// §4: a stored value that no longer parses keeps raw (fail open) and
// `clio index` warns, naming the value.
func TestIndexWarnsOnBadRetentionAndKeepsRaw(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "abc")
	database.Close()
	oldErr := os.Stderr
	rerr, werr, _ := os.Pipe()
	os.Stderr = werr
	_, err := runIndexCmd(t, "--full")
	werr.Close()
	os.Stderr = oldErr
	var eb bytes.Buffer
	eb.ReadFrom(rerr)
	if err != nil {
		t.Fatalf("index --full: %v\n%s", err, eb.String())
	}
	if !strings.Contains(eb.String(), "value=abc") {
		t.Fatalf("no warning naming the bad value:\n%s", eb.String())
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] == "" {
		t.Fatal("bad retention value blanked raw_json")
	}
}

// A session written while retention was off (raw kept) and older than the window
// is pruned by the sweep at the end of `clio index`.
func TestIndexSweepPrunesAgedSession(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	out, err := runIndexCmd(t)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	if !strings.Contains(out, "raw retention 14d: pruned 1 messages across 1 sessions") {
		t.Fatalf("missing sweep summary:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] != "" {
		t.Fatalf("aged session not swept: %q", raw[0])
	}
}

// A session whose transcript went missing is skipped by the sweep (prune-raw's
// "missing" reason), and a skip prints nothing.
func TestIndexSweepSkipsMissingSource(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	out, err := runIndexCmd(t)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	if strings.Contains(out, "raw retention") {
		t.Fatalf("sweep reported a skipped session:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] == "" {
		t.Fatal("missing-source session was swept")
	}
}

// The catch-ups that run before reads — the MCP per-call one (beforeRead,
// mcp.go:181) and the CLI one (runCatchUp, common.go:108) — do not sweep: an aged
// session stays intact through them, and `clio index` prunes it.
func TestReadCatchUpsDoNotSweep(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	projects := filepath.Dir(filepath.Dir(path))
	var ing leaderIngester = ingest.New(database, nil)
	var notLeader atomic.Bool
	newBeforeRead(context.Background(), testEnv(ing, time.Hour), &notLeader, projects, discardLogger())(context.Background())
	runCatchUp(context.Background(), ing, projects, discardLogger())
	if raw := rawJSONOf(t, database, "sess-old"); raw[0] == "" {
		t.Fatal("a read catch-up swept an aged session")
	}
	database.Close()
	if _, err := runIndexCmd(t); err != nil {
		t.Fatal(err)
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] != "" {
		t.Fatal("clio index did not sweep the session the catch-ups left")
	}
}

// §4: a sweep failure does not undo the ingest that already committed, and
// `clio index` exits non-zero. The sweep's pending-backfill read is made to fail
// by renaming the watermark table it queries; ingest does not need that table.
func TestIndexSweepFailureExitsNonZeroAfterIngest(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	if _, err := database.Exec(`ALTER TABLE activity_backfill_state RENAME TO activity_backfill_state_gone`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	line := `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","cwd":"/p","sessionId":"sess-new","message":{"role":"user","content":"new work"}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "sess-new.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runIndexCmd(t)
	if err == nil || !strings.Contains(err.Error(), "raw retention sweep failed") {
		t.Fatalf("err = %v, want the sweep failure", err)
	}
	d := reopen(t)
	defer d.Close()
	if len(rawJSONOf(t, d, "sess-new")) == 0 {
		t.Fatal("the ingest before the failed sweep was not committed")
	}
}

// A session whose file no longer matches the stored snapshot (prune-raw's
// "changed" reason) is skipped; with the snapshot matching again it is pruned.
// `clio index` would re-ingest a changed file before the sweep, so the sweep is
// driven directly here.
func TestSweepRawRetentionSkipsChangedSource(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	roots := []string{filepath.Dir(filepath.Dir(path))}
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if _, err := database.Exec(`UPDATE ingest_state SET last_mtime = last_mtime - 1 WHERE source_file = ?`, path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := sweepRawRetention(context.Background(), database, roots, cutoff, "14d", &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || rawJSONOf(t, database, "sess-old")[0] == "" {
		t.Fatalf("changed session swept (out=%q)", out.String())
	}
	if _, err := database.Exec(`UPDATE ingest_state SET last_mtime = last_mtime + 1 WHERE source_file = ?`, path); err != nil {
		t.Fatal(err)
	}
	if err := sweepRawRetention(context.Background(), database, roots, cutoff, "14d", &out); err != nil {
		t.Fatal(err)
	}
	if rawJSONOf(t, database, "sess-old")[0] != "" {
		t.Fatalf("matching session not swept (out=%q)", out.String())
	}
}

// The MCP leader's startup catch-up (mcp.go:351) sweeps once, through the
// runtime newMCPRuntime builds for `clio mcp`.
func TestMCPStartupCatchUpSweeps(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	projects := filepath.Dir(filepath.Dir(path))
	rt := newMCPRuntime(database, projects, discardLogger(), nil)
	env := testEnv(rt.ing, time.Hour)
	env.sweep = rt.sweep
	var flag atomic.Bool
	if !startupCatchup(context.Background(), &flag, env, projects, discardLogger(), false) {
		t.Fatal("startup catch-up cancelled")
	}
	if raw := rawJSONOf(t, database, "sess-old"); raw[0] != "" {
		t.Fatal("MCP startup catch-up did not sweep the aged session")
	}
}

// A failed startup sweep is logged and the leader still starts.
func TestMCPStartupSweepFailureOnlyLogs(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	projects := filepath.Dir(filepath.Dir(path))
	env := testEnv(ingest.New(database, nil), time.Hour)
	env.sweep = func(context.Context, ingest.Stats) error { return errors.New("boom") }
	var logBuf bytes.Buffer
	var flag atomic.Bool
	if !startupCatchup(context.Background(), &flag, env, projects, slog.New(slog.NewTextHandler(&logBuf, nil)), false) || !flag.Load() {
		t.Fatal("a sweep failure stopped the leader's startup")
	}
	if !strings.Contains(logBuf.String(), "raw retention sweep failed") || !strings.Contains(logBuf.String(), "boom") {
		t.Fatalf("sweep failure not logged:\n%s", logBuf.String())
	}
}
