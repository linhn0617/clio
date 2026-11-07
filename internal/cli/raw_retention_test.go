package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

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

// The whole `clio mcp` command, with the runtime newMCPRuntime really builds
// (its ingester and its sweep), sweeps an aged session at leader startup: this
// pins the wiring in newMCPCmd (env.sweep = rt.sweep) and the sweep
// newMCPRuntime returns. Only the watcher is replaced, to signal that the
// startup catch-up has finished (the leader starts it right after).
func TestMCPCommandStartupSweepsAgedSession(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	origRT, origServe := newMCPRuntime, serveMCP
	t.Cleanup(func() { newMCPRuntime, serveMCP = origRT, origServe })
	caughtUp := make(chan struct{})
	var once sync.Once
	newMCPRuntime = func(rw *db.DB, projects string, log *slog.Logger, guard func() func()) mcpRuntime {
		rt := origRT(rw, projects, log, guard)
		rt.watch = func(ctx context.Context) {
			once.Do(func() { close(caughtUp) })
			<-ctx.Done()
		}
		return rt
	}
	serveMCP = func(*server.MCPServer) error {
		select {
		case <-caughtUp:
		case <-time.After(30 * time.Second):
			t.Error("leader startup catch-up did not finish")
		}
		return nil
	}
	cmd := newMCPCmd()
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] != "" {
		t.Fatal("clio mcp leader startup did not sweep the aged session")
	}
}

// failingIngester is a fakeIngester whose IngestAll fails outright.
type failingIngester struct{ *fakeIngester }

func (f failingIngester) IngestAll(context.Context, string, bool) (ingest.Stats, error) {
	f.ingestAll.Add(1)
	return ingest.Stats{RawCutoff: 1, RawRetention: "14d"}, errors.New("walk failed")
}

// When the startup IngestAll fails the sweep is skipped (its Stats are not
// trusted), and the leader flag is still published.
func TestMCPStartupSkipsSweepWhenIngestFails(t *testing.T) {
	fake := failingIngester{&fakeIngester{}}
	env := testEnv(fake, time.Hour)
	var sweeps atomic.Int32
	env.sweep = func(context.Context, ingest.Stats) error { sweeps.Add(1); return nil }
	var flag atomic.Bool
	if !startupCatchup(context.Background(), &flag, env, t.TempDir(), discardLogger(), false) || !flag.Load() {
		t.Fatal("a failed startup ingest stopped the leader's startup")
	}
	if fake.ingestAll.Load() != 1 {
		t.Fatalf("IngestAll calls = %d, want 1", fake.ingestAll.Load())
	}
	if n := sweeps.Load(); n != 0 {
		t.Fatalf("sweep ran %d time(s) after a failed startup ingest, want 0", n)
	}
}

// §3.4 restore path: with retention set, --full blanks the old session and exits
// 0; after `config raw-retention off`, --full restores it.
func TestRetentionOffThenFullRestores(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	if out, err := runIndexCmd(t, "--full"); err != nil {
		t.Fatalf("index --full with retention: %v\n%s", err, out)
	}
	d := reopen(t)
	if rawJSONOf(t, d, "sess-old")[0] != "" {
		t.Fatal("--full kept raw_json of a session outside the window")
	}
	d.Close()
	if _, err := runConfigCmd(t, "raw-retention", "off"); err != nil {
		t.Fatal(err)
	}
	if out, err := runIndexCmd(t, "--full"); err != nil {
		t.Fatalf("index --full after off: %v\n%s", err, out)
	}
	d = reopen(t)
	defer d.Close()
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("retention off + --full did not restore raw_json")
	}
}

func TestHelpTextNamesRetentionRestorePath(t *testing.T) {
	const hint = "clio config raw-retention off"
	if !strings.Contains(newPruneRawCmd().Long, hint) {
		t.Errorf("prune-raw help lacks %q", hint)
	}
	if f := newIndexCmd().Flags().Lookup("full"); f == nil || !strings.Contains(f.Usage, hint) {
		t.Errorf("index --full help lacks %q", hint)
	}
}

// §3.4: sessions the active retention says must be blank are not residuals; a
// pruned session inside the window still is, and so is any pruned session when
// retention is off.
func TestCheckPrunedResidualsExcludesRetentionBlanked(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30) // last activity 30 days ago
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")
	var errOut bytes.Buffer
	cutoff14 := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if err := checkPrunedResiduals(context.Background(), database, &errOut, nil, cutoff14); err != nil {
		t.Fatalf("retention 14d: err=%v for a session it must blank\n%s", err, errOut.String())
	}
	errOut.Reset()
	cutoff60 := time.Now().Add(-60 * 24 * time.Hour).Unix()
	if err := checkPrunedResiduals(context.Background(), database, &errOut, nil, cutoff60); err == nil || !strings.Contains(errOut.String(), "sess-old") {
		t.Fatalf("retention 60d: err=%v stderr=%q; want the in-window session reported", err, errOut.String())
	}
	errOut.Reset()
	if err := checkPrunedResiduals(context.Background(), database, &errOut, nil, 0); err == nil || !strings.Contains(errOut.String(), "sess-old") {
		t.Fatalf("retention off: err=%v stderr=%q; want the residual reported", err, errOut.String())
	}
}

func TestPrunedNoteMentionsRetention(t *testing.T) {
	var b bytes.Buffer
	prunedNote(&b, "abcd1234", "")
	if got, want := b.String(), "note: raw form of session abcd1234 was pruned to save space — restore it with `clio index --full`\n"; got != want {
		t.Fatalf("retention off: %q, want the unchanged note %q", got, want)
	}
	b.Reset()
	prunedNote(&b, "abcd1234", "14d")
	if !strings.Contains(b.String(), "raw retention is 14d") || !strings.Contains(b.String(), "clio config raw-retention off") {
		t.Fatalf("retention 14d: %q lacks the retention restore hint", b.String())
	}
}

func TestShowRawNoteNamesRetention(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")
	putRetention(t, database, "14d")
	database.Close()
	oldErr := os.Stderr
	rerr, werr, _ := os.Pipe()
	os.Stderr = werr
	runShowCmd(t, "sess-old", "--format", "raw", "--source", "all")
	werr.Close()
	os.Stderr = oldErr
	var eb bytes.Buffer
	eb.ReadFrom(rerr)
	if !strings.Contains(eb.String(), "raw retention is 14d") {
		t.Fatalf("raw show note does not mention the retention setting:\n%s", eb.String())
	}
}

// With retention on, the sweep has already blanked old sessions, so prune-raw
// finds nothing to prune; --vacuum is still the documented way to shrink the
// file and must VACUUM anyway.
func TestPruneRawVacuumRunsWhenNothingPruned(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")
	putRetention(t, database, "14d")
	// Leave free pages behind, as a pruned and FTS-optimized index does.
	if _, err := database.Exec(`CREATE TABLE junk AS SELECT randomblob(4096) AS b FROM (WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 256) SELECT x FROM c)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM junk`); err != nil {
		t.Fatal(err)
	}
	var free int64
	database.QueryRow(`PRAGMA freelist_count`).Scan(&free)
	if free == 0 {
		t.Fatal("setup: no free pages to reclaim")
	}
	database.Close()

	out := runPruneCmd(t, "--older-than", "14d", "--vacuum", "--source", "all")
	if !strings.Contains(out, "pruned 0 messages") || !strings.Contains(out, "VACUUM done") {
		t.Fatalf("prune-raw --vacuum with nothing to prune did not VACUUM:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	d.QueryRow(`PRAGMA freelist_count`).Scan(&free)
	if free != 0 {
		t.Fatalf("freelist_count=%d after --vacuum, want 0", free)
	}
}

// prune-raw's closing restore hint must not send the user to `clio index --full`
// alone while a retention window would keep the session blank.
func TestPruneRawRestoreHintFollowsRetention(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	database.Close()
	out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
	if !strings.Contains(out, "restore the raw form anytime with `clio index --full`") {
		t.Fatalf("retention off: missing the plain restore hint:\n%s", out)
	}

	database, _ = pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	out = runPruneCmd(t, "--older-than", "14d", "--source", "all")
	if !strings.Contains(out, "pruned 1 messages") {
		t.Fatalf("retention 14d: setup did not prune:\n%s", out)
	}
	if strings.Contains(out, "restore the raw form anytime with `clio index --full`") ||
		!strings.Contains(out, "raw retention is 14d") ||
		!strings.Contains(out, "`clio config raw-retention off`, then `clio index --full`") {
		t.Fatalf("retention 14d: restore hint ignores retention:\n%s", out)
	}
}

// Under --full, a residual failure and a sweep failure are both returned, and the
// sweep's error keeps its "raw retention sweep failed" prefix.
func TestIndexFullJoinsResidualAndSweepFailure(t *testing.T) {
	database, path := pruneSandbox(t, "sess-in", 5) // inside a 14d window
	mustPrune(t, database, time.Now().Add(-24*time.Hour).Unix(), "sess-in")
	putRetention(t, database, "14d")
	if _, err := database.Exec(`ALTER TABLE activity_backfill_state RENAME TO activity_backfill_state_gone`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	// Unreadable: --full cannot restore it, so it stays a residual.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)
	_, err := runIndexCmd(t, "--full")
	if err == nil || !strings.Contains(err.Error(), "could not be restored") {
		t.Fatalf("err = %v, want the residual failure", err)
	}
	if !strings.Contains(err.Error(), "raw retention sweep failed:") {
		t.Fatalf("err = %q, want the sweep failure with its prefix", err)
	}
}

// e2e4: a live session that started outside the window receives a line with no
// timestamp; `clio index` must neither clear its ended_at nor sweep its raw_json.
func TestIndexSweepKeepsLiveSessionAfterUntimestampedAppend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	projects := filepath.Join(home, ".claude", "projects", "-Users-x-proj")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projects, "sess-live.jsonl")
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	var lines []string
	for i, ts := range []string{old, old, old, now} { // past the 512-byte fingerprint window
		lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"u%d","timestamp":"%s","cwd":"/p","sessionId":"sess-live","message":{"role":"user","content":"live session line %d with some padding text"}}`, i, ts, i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runConfigCmd(t, "raw-retention", "14d"); err != nil {
		t.Fatal(err)
	}
	if out, err := runIndexCmd(t); err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"summary","summary":"live work","leafUuid":"u3"}` + "\n")
	f.Close()
	out, err := runIndexCmd(t)
	if err != nil {
		t.Fatalf("index after append: %v\n%s", err, out)
	}
	if strings.Contains(out, "raw retention") {
		t.Fatalf("sweep pruned the live session:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	for i, raw := range rawJSONOf(t, d, "sess-live") {
		if raw == "" {
			t.Fatalf("row %d of the live session lost raw_json", i)
		}
	}
}
