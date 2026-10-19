package doctor

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/lock"
)

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeBin creates a file with the given mode and returns its path.
func fakeBin(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func settingsWith(recall, fh string) string {
	return fmt.Sprintf(`{"hooks":{
	  "SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":%q}]}],
	  "PreToolUse":[{"matcher":"Read|Bash","hooks":[{"type":"command","command":%q}]}]}}`, recall, fh)
}

func TestHookBinaryCheck(t *testing.T) {
	dir := t.TempDir()
	good := fakeBin(t, dir, "clio", 0o755)
	// The recogniser requires the basename "clio", so use a separate directory.
	neDir := filepath.Join(dir, "ne")
	if err := os.MkdirAll(neDir, 0o755); err != nil {
		t.Fatal(err)
	}
	noExec := fakeBin(t, neDir, "clio", 0o644)
	settings := filepath.Join(dir, "settings.json")

	t.Run("settings file absent is ok not installed", func(t *testing.T) {
		r := checkHookBinary(filepath.Join(dir, "nope.json"))
		if !r.OK || !strings.Contains(r.Detail, "not installed") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("no clio hooks is ok not installed", func(t *testing.T) {
		writeJSON(t, settings, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/bin/other x"}]}]}}`)
		r := checkHookBinary(settings)
		if !r.OK || !strings.Contains(r.Detail, "not installed") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("both hooks point at an executable", func(t *testing.T) {
		writeJSON(t, settings, settingsWith(good+" recall", good+" file-history --hook"))
		r := checkHookBinary(settings)
		if !r.OK || !strings.Contains(r.Detail, good) {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("missing binary warns with reinstall hint", func(t *testing.T) {
		gone := filepath.Join(dir, "gone", "clio")
		writeJSON(t, settings, settingsWith(gone+" recall", good+" file-history --hook"))
		r := checkHookBinary(settings)
		want := "hook points to missing binary " + gone + "; rerun clio install-hook"
		if r.OK || !strings.Contains(r.Detail, want) {
			t.Fatalf("want WARN containing %q, got %+v", want, r)
		}
	})
	t.Run("file-history hook with missing binary warns", func(t *testing.T) {
		gone := filepath.Join(dir, "gone2", "clio")
		writeJSON(t, settings, settingsWith(good+" recall", gone+" file-history --hook"))
		if r := checkHookBinary(settings); r.OK || !strings.Contains(r.Detail, gone) {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("non-executable file warns", func(t *testing.T) {
		writeJSON(t, settings, settingsWith(noExec+" recall", good+" file-history --hook"))
		r := checkHookBinary(settings)
		if r.OK || !strings.Contains(r.Detail, "not executable") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("quoted path with spaces is resolved", func(t *testing.T) {
		sp := filepath.Join(dir, "a b")
		if err := os.MkdirAll(sp, 0o755); err != nil {
			t.Fatal(err)
		}
		bin := fakeBin(t, sp, "clio", 0o755)
		writeJSON(t, settings, settingsWith("'"+bin+"' recall", "'"+bin+"' file-history --hook"))
		if r := checkHookBinary(settings); !r.OK {
			t.Fatalf("got %+v", r)
		}
	})
	// The shell splits an unquoted path at the space and exits 127, so the hook
	// cannot run even though the binary exists.
	t.Run("legacy unquoted path with spaces warns even though the binary exists", func(t *testing.T) {
		sp := filepath.Join(dir, "c d")
		if err := os.MkdirAll(sp, 0o755); err != nil {
			t.Fatal(err)
		}
		bin := fakeBin(t, sp, "clio", 0o755)
		writeJSON(t, settings, settingsWith(bin+" recall", "'"+bin+"' file-history --hook"))
		r := checkHookBinary(settings)
		if r.OK || !strings.Contains(r.Detail, "recall hook has an unquoted path with whitespace; rerun clio install-hook") {
			t.Fatalf("want WARN for the unquoted recall hook, got %+v", r)
		}
		writeJSON(t, settings, settingsWith("'"+bin+"' recall", bin+" file-history --hook"))
		r = checkHookBinary(settings)
		if r.OK || !strings.Contains(r.Detail, "file-history hook has an unquoted path with whitespace; rerun clio install-hook") {
			t.Fatalf("want WARN for the unquoted file-history hook, got %+v", r)
		}
	})
	// Upgrading the binary without rerunning install-hook leaves the older shape
	// behind: one of the two hooks missing, or file-history under matcher "Read".
	t.Run("half install warns naming the missing hook", func(t *testing.T) {
		writeJSON(t, settings, `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"`+good+` recall"}]}]}}`)
		r := checkHookBinary(settings)
		if r.OK || !strings.Contains(r.Detail, "file-history hook not installed; rerun clio install-hook") {
			t.Fatalf("recall only: got %+v", r)
		}
		writeJSON(t, settings, `{"hooks":{"PreToolUse":[{"matcher":"Read|Bash","hooks":[{"type":"command","command":"`+good+` file-history --hook"}]}]}}`)
		r = checkHookBinary(settings)
		if r.OK || !strings.Contains(r.Detail, "recall hook not installed; rerun clio install-hook") {
			t.Fatalf("file-history only: got %+v", r)
		}
	})
	t.Run("old Read matcher needs upgrade", func(t *testing.T) {
		writeJSON(t, settings, `{"hooks":{
		  "SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"`+good+` recall"}]}],
		  "PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"`+good+` file-history --hook"}]}]}}`)
		r := checkHookBinary(settings)
		if r.OK || !strings.Contains(r.Detail, "file-history hook matcher is Read, needs upgrade; rerun clio install-hook") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("ok detail reports each hook separately", func(t *testing.T) {
		writeJSON(t, settings, settingsWith(good+" recall", good+" file-history --hook"))
		r := checkHookBinary(settings)
		if want := "recall: " + good + "; file-history: " + good; !r.OK || r.Detail != want {
			t.Fatalf("want ok %q, got %+v", want, r)
		}
	})
	t.Run("non-absolute commands warn without PATH or cwd lookup", func(t *testing.T) {
		for _, bin := range []string{"clio", "./clio", "~/clio", "$HOME/clio", "bin/clio"} {
			writeJSON(t, settings, settingsWith(bin+" recall", good+" file-history --hook"))
			r := checkHookBinary(settings)
			if r.OK || !strings.Contains(r.Detail, "hook command is not an absolute path; rerun clio install-hook") {
				t.Errorf("%q: want not-absolute WARN, got %+v", bin, r)
			}
		}
	})
	t.Run("malformed settings warns instead of crashing", func(t *testing.T) {
		writeJSON(t, settings, `{oops`)
		if r := checkHookBinary(settings); r.OK {
			t.Fatalf("got %+v", r)
		}
	})
}

// Windows has no exec bit: an existing regular file is enough there, a directory
// never is.
func TestBinaryProblemPlatformRules(t *testing.T) {
	dir := t.TempDir()
	plain := fakeBin(t, dir, "clio", 0o644)
	if p := binaryProblemOS(plain, "linux"); !strings.Contains(p, "not executable") {
		t.Fatalf("linux: 0644 file must be flagged, got %q", p)
	}
	if p := binaryProblemOS(plain, "windows"); p != "" {
		t.Fatalf("windows: existing regular file must pass, got %q", p)
	}
	if p := binaryProblemOS(dir, "windows"); p == "" {
		t.Fatal("windows: a directory must still be flagged")
	}
}

func TestMCPRegistrationCheck(t *testing.T) {
	dir := t.TempDir()
	good := fakeBin(t, dir, "clio", 0o755)
	cfg := filepath.Join(dir, ".claude.json")

	t.Run("config absent is ok and not a warning", func(t *testing.T) {
		r := checkMCPRegistration(filepath.Join(dir, "none.json"))
		if !r.OK || !strings.Contains(r.Detail, "not registered (clio install-mcp)") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("other servers only is ok not registered", func(t *testing.T) {
		writeJSON(t, cfg, `{"mcpServers":{"x":{"command":"/x"}}}`)
		r := checkMCPRegistration(cfg)
		if !r.OK || !strings.Contains(r.Detail, "not registered") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("registered with live binary lists the path", func(t *testing.T) {
		writeJSON(t, cfg, fmt.Sprintf(`{"mcpServers":{"clio":{"command":%q,"args":["mcp"]}}}`, good))
		r := checkMCPRegistration(cfg)
		if !r.OK || !strings.Contains(r.Detail, good) {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("registered with missing binary warns", func(t *testing.T) {
		gone := filepath.Join(dir, "gone", "clio")
		writeJSON(t, cfg, fmt.Sprintf(`{"mcpServers":{"clio":{"command":%q}}}`, gone))
		r := checkMCPRegistration(cfg)
		if r.OK || !strings.Contains(r.Detail, "missing binary "+gone) {
			t.Fatalf("got %+v", r)
		}
	})
	// install-mcp falls back to a bare `clio`; Claude Code resolves it on its own
	// PATH, which doctor cannot see, so it is a note, never a WARN.
	t.Run("non-absolute command is ok with a PATH note", func(t *testing.T) {
		const note = "resolved on doctor's PATH, not Claude Code's; prefer an absolute path"
		t.Setenv("PATH", "")
		for _, c := range []string{"clio", "bin/clio"} {
			writeJSON(t, cfg, fmt.Sprintf(`{"mcpServers":{"clio":{"command":%q}}}`, c))
			r := checkMCPRegistration(cfg)
			if !r.OK || !strings.Contains(r.Detail, "registered: "+c) || !strings.Contains(r.Detail, "not found on doctor's PATH, which is not Claude Code's; prefer an absolute path") {
				t.Errorf("%q not on PATH: want ok with note, got %+v", c, r)
			}
		}
		t.Setenv("PATH", dir)
		writeJSON(t, cfg, `{"mcpServers":{"clio":{"command":"clio"}}}`)
		r := checkMCPRegistration(cfg)
		if !r.OK || !strings.Contains(r.Detail, "-> "+good) || !strings.Contains(r.Detail, note) {
			t.Fatalf("clio on PATH: want ok naming %s with note, got %+v", good, r)
		}
	})
	t.Run("url-based entry has no local binary to check", func(t *testing.T) {
		writeJSON(t, cfg, `{"mcpServers":{"clio":{"type":"sse","url":"http://localhost:1/sse"}}}`)
		r := checkMCPRegistration(cfg)
		if !r.OK || !strings.Contains(r.Detail, "registered (url-based, no local binary to check)") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("malformed entries warn", func(t *testing.T) {
		for _, body := range []string{`{"mcpServers":{"clio":null}}`, `{"mcpServers":{"clio":"oops"}}`, `{"mcpServers":{"clio":{"command":3}}}`} {
			writeJSON(t, cfg, body)
			r := checkMCPRegistration(cfg)
			if r.OK || !strings.Contains(r.Detail, "malformed mcpServers.clio entry; rerun clio install-mcp") {
				t.Errorf("%s: got %+v", body, r)
			}
		}
	})
	t.Run("unparsable config warns without modify wording", func(t *testing.T) {
		writeJSON(t, cfg, `{"mcpServers":"nope"}`)
		r := checkMCPRegistration(cfg)
		if r.OK || strings.Contains(r.Detail, "refusing to modify") {
			t.Fatalf("got %+v", r)
		}
		writeJSON(t, cfg, `{oops`)
		if r := checkMCPRegistration(cfg); r.OK {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestLeaseCheckStates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.lock")

	if r := checkLease(path); !r.OK || !strings.Contains(r.Detail, "no mcp running") {
		t.Fatalf("absent lease: %+v", r)
	}

	// Fresh heartbeat from a live pid (this test process) via the real lease API.
	l, leader, err := lock.AcquireOrFollow(path)
	if err != nil || !leader {
		t.Fatalf("acquire: leader=%v err=%v", leader, err)
	}
	defer l.Release()
	if r := checkLease(path); !r.OK || !strings.Contains(r.Detail, fmt.Sprintf("mcp leader pid %d", os.Getpid())) {
		t.Fatalf("fresh lease: %+v", r)
	}

	// Live pid, heartbeat past the TTL: a leader that may just be catching up.
	old := time.Now().Add(-5 * time.Minute).Unix()
	writeJSON(t, path, fmt.Sprintf("%d 42 %d", os.Getpid(), old))
	r := checkLease(path)
	want := fmt.Sprintf("pid %d alive (leader may be catching up)", os.Getpid())
	if r.OK || !strings.Contains(r.Detail, "heartbeat 30") || !strings.Contains(r.Detail, want) {
		t.Fatalf("late-but-alive lease: want WARN with %q, got %+v", want, r)
	}

	// Dead pid, even with a fresh heartbeat: stale, with the remedy and the path.
	const deadPid = 2147483646
	writeJSON(t, path, fmt.Sprintf("%d 42 %d", deadPid, time.Now().Unix()))
	r = checkLease(path)
	if r.OK || !strings.Contains(r.Detail, fmt.Sprintf("stale lease from pid %d (dead); restart Claude Code or remove %s", deadPid, path)) {
		t.Fatalf("dead-pid lease: %+v", r)
	}

	writeJSON(t, path, "garbage")
	if r := checkLease(path); r.OK || !strings.Contains(r.Detail, "unreadable") {
		t.Fatalf("malformed lease must warn: %+v", r)
	}
}

// ingestRow records a tracked file as ingested up to (size, offset, mtime).
func ingestRow(t *testing.T, d *db.DB, path string, lastSize, offset, lastMtime, ingestedAt int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		path, lastSize, lastMtime, offset, "fp", ingestedAt); err != nil {
		t.Fatal(err)
	}
}

// freshness judges reconcile's numbers instead of walking the files again, so the
// two can never disagree.
func freshnessAt(t *testing.T, d *db.DB, root string, now time.Time) Result {
	t.Helper()
	st, err := reconcile(d, []string{root}, now)
	return checkIndexFreshness(st, err)
}

func TestIndexFreshnessJudgesReconcileAge(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()

	if r := freshnessAt(t, d, dir, now); r.OK || !strings.Contains(r.Detail, "never indexed; run clio index") {
		t.Fatalf("empty ingest_state: %+v", r)
	}

	mk := func(name, body string, mtimeAgo time.Duration) (string, os.FileInfo) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-mtimeAgo)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(p)
		return p, fi
	}
	drop := func(paths ...string) {
		for _, p := range paths {
			if _, err := d.Exec(`DELETE FROM ingest_state WHERE source_file = ?`, p); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 1. A fully absorbed file whose last ingest was 3 days ago: the user simply did
	// not use clio. Must be ok — `clio index` cannot (and need not) refresh this.
	p1, fi1 := mk("absorbed.jsonl", "0123456789\n", 72*time.Hour)
	ingestRow(t, d, p1, fi1.Size(), fi1.Size(), fi1.ModTime().UnixNano(), now.Add(-72*time.Hour).Unix())
	if r := freshnessAt(t, d, dir, now); !r.OK || r.Detail != "no unindexed changes" {
		t.Fatalf("quiet-but-complete index must be ok: %+v", r)
	}

	// 2. New lines appended 1h ago and not yet absorbed: within the grace window.
	p2, fi2 := mk("recent.jsonl", "0123456789\nABCDEF\n", 1*time.Hour)
	ingestRow(t, d, p2, 11, 11, fi2.ModTime().UnixNano()-1, now.Add(-2*time.Hour).Unix())
	if r := freshnessAt(t, d, dir, now); !r.OK || !strings.Contains(r.Detail, "oldest unindexed change 1h old") {
		t.Fatalf("unabsorbed lines under 24h old must still be ok: %+v", r)
	}

	// 3. New lines whose file was last written 30h ago.
	p3, fi3 := mk("behind.jsonl", "0123456789\nABCDEF\n", 30*time.Hour)
	ingestRow(t, d, p3, 11, 11, fi3.ModTime().UnixNano()-1, now.Add(-72*time.Hour).Unix())
	if r := freshnessAt(t, d, dir, now); r.OK || !strings.Contains(r.Detail, "oldest unindexed change 30h old; run clio index") {
		t.Fatalf("unindexed lines 30h old must warn: %+v", r)
	}

	// 4. A same-size rewrite (mtime moved, size equal) 40h ago also counts, and the
	// oldest change is the one reported.
	p4, fi4 := mk("rewritten.jsonl", "0123456789\n", 40*time.Hour)
	ingestRow(t, d, p4, fi4.Size(), fi4.Size(), fi4.ModTime().UnixNano()-5, now.Add(-72*time.Hour).Unix())
	if r := freshnessAt(t, d, dir, now); r.OK || !strings.Contains(r.Detail, "oldest unindexed change 40h old") {
		t.Fatalf("oldest change (40h) must be reported: %+v", r)
	}
	drop(p3, p4)

	// 5. A truncated file is a change too.
	p5, fi5 := mk("truncated.jsonl", "01\n", 45*time.Hour)
	ingestRow(t, d, p5, 100, 100, fi5.ModTime().UnixNano(), now.Add(-72*time.Hour).Unix())
	if r := freshnessAt(t, d, dir, now); r.OK || !strings.Contains(r.Detail, "oldest unindexed change 45h old") {
		t.Fatalf("truncation must count: %+v", r)
	}
	drop(p5)

	// 6. A tracked file that no longer exists is not a change (reconciliation owns that).
	ingestRow(t, d, filepath.Join(dir, "gone.jsonl"), 10, 10, 1, now.Add(-72*time.Hour).Unix())
	if r := freshnessAt(t, d, dir, now); !r.OK {
		t.Fatalf("missing tracked file must not trip freshness: %+v", r)
	}
}

// A session that stopped mid-write leaves a last line without its newline. Ingest
// only absorbs complete lines, so `clio index` can never clear those bytes; they
// must not make freshness warn, but the detail still says why reconciliation
// counts them.
func TestIndexFreshnessIgnoresPartialLastLine(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()
	p := filepath.Join(dir, "s.jsonl")
	body := "{\"a\":1}\n{\"b\":"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := now.Add(-30 * time.Hour)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	// What ingest records: size and mtime seen, offset just past the last newline.
	ingestRow(t, d, p, fi.Size(), int64(strings.IndexByte(body, '\n')+1), fi.ModTime().UnixNano(), mt.Unix())

	r := freshnessAt(t, d, dir, now)
	if !r.OK || !strings.Contains(r.Detail, "no unindexed changes (1 file(s) end in a partial line") {
		t.Fatalf("partial last line must not warn: %+v", r)
	}
	res := Run(d, dir, dbPath)
	if rec := findResult(t, res, "source reconciliation"); !strings.Contains(rec.Detail, "1 with new unindexed bytes") {
		t.Fatalf("reconciliation still counts the bytes: %+v", rec)
	}

	// Once the line is completed it is a real change again.
	if err := os.WriteFile(p, []byte(body+"2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	if r := freshnessAt(t, d, dir, now); r.OK || !strings.Contains(r.Detail, "oldest unindexed change 30h old; run clio index") {
		t.Fatalf("completed line must count: %+v", r)
	}
}

func newQuickDB(t *testing.T) (*db.DB, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, dir, dbPath
}

// The heavy scans are recorded as they are started, so a regression that runs one in
// --quick (for example restoring integrity_check) turns this red.
func TestQuickModeRunsNoIntegrityScan(t *testing.T) {
	d, dir, dbPath := newQuickDB(t)

	var full []string
	RunWith(d, dir, dbPath, Options{onHeavy: func(n string) { full = append(full, n) }})
	sort.Strings(full)
	if strings.Join(full, ",") != "dbstat,fts-integrity-check,integrity_check" {
		t.Fatalf("full run must start all three heavy scans, got %v", full)
	}

	var quick []string
	res := RunWith(d, dir, dbPath, Options{Quick: true, onHeavy: func(n string) { quick = append(quick, n) }})
	if len(quick) != 0 {
		t.Fatalf("--quick must not start any heavy scan, started %v", quick)
	}
	for _, name := range []string{"db integrity", "fts content check", "fts bloat"} {
		r := findResult(t, res, name)
		if !r.Skipped || !r.OK || !strings.Contains(r.Detail, "--quick") {
			t.Fatalf("%q must be a skip mentioning --quick, got %+v", name, r)
		}
	}
	// The cheap row-count comparison still runs.
	if r := findResult(t, res, "fts index"); !r.OK || strings.Contains(r.Detail, "content verified") {
		t.Fatalf("fts index in quick mode: %+v", r)
	}
}

// Even when the row-count comparison errors, the content check was not run and must
// still be listed as skipped. (External-content FTS counts read the content table,
// so a plain count mismatch is unreachable; the query error is the live path.)
func TestQuickModeStillListsFTSContentSkipWhenCountsFail(t *testing.T) {
	d, dir, dbPath := newQuickDB(t)
	if _, err := d.Exec("DROP TABLE messages"); err != nil {
		t.Fatal(err)
	}
	res := RunWith(d, dir, dbPath, Options{Quick: true})
	if r := findResult(t, res, "fts index"); r.OK {
		t.Fatalf("fixture: query should error, got %+v", r)
	}
	if r := findResult(t, res, "fts content check"); !r.Skipped {
		t.Fatalf("content check skip row missing on query error: %+v", r)
	}
}

func TestIntegrationChecksRunInBothModes(t *testing.T) {
	d, dir, dbPath := newQuickDB(t)
	opts := Options{
		Integration:      true,
		SettingsPath:     filepath.Join(dir, "settings.json"),
		ClaudeConfigPath: filepath.Join(dir, ".claude.json"),
		LockPath:         filepath.Join(dir, "mcp.lock"),
	}
	for _, quick := range []bool{false, true} {
		opts.Quick = quick
		res := RunWith(d, dir, dbPath, opts)
		for _, name := range []string{"hook binary", "mcp registration", "mcp lease", "index freshness"} {
			if r := findResult(t, res, name); !r.Integration {
				t.Fatalf("%q must be marked Integration", name)
			}
		}
	}
	// Plain Run (legacy callers/tests) stays hermetic: no HOME-dependent checks.
	for _, r := range Run(d, dir, dbPath) {
		if r.Name == "hook binary" || r.Name == "mcp registration" {
			t.Fatalf("Run must not read the real HOME, got %q", r.Name)
		}
	}
}

// One path that cannot be resolved only takes out its own check.
func TestUnresolvedPathAffectsOnlyThatCheck(t *testing.T) {
	d, dir, dbPath := newQuickDB(t)
	good := Options{
		Integration:      true,
		SettingsPath:     filepath.Join(dir, "settings.json"),
		ClaudeConfigPath: filepath.Join(dir, ".claude.json"),
		LockPath:         filepath.Join(dir, "mcp.lock"),
	}
	cases := map[string]func(o *Options){
		"hook binary":      func(o *Options) { o.SettingsPath = "" },
		"mcp registration": func(o *Options) { o.ClaudeConfigPath = "" },
		"mcp lease":        func(o *Options) { o.LockPath = "" },
	}
	for broken, mutate := range cases {
		o := good
		mutate(&o)
		res := RunWith(d, dir, dbPath, o)
		for _, name := range []string{"hook binary", "mcp registration", "mcp lease", "index freshness"} {
			r := findResult(t, res, name)
			if name == broken {
				if r.OK || !strings.Contains(r.Detail, "cannot resolve") {
					t.Errorf("%s: want WARN 'cannot resolve', got %+v", name, r)
				}
			} else if strings.Contains(r.Detail, "cannot resolve") {
				t.Errorf("%s must still run when %s is unresolved, got %+v", name, broken, r)
			}
		}
	}
}

// snapshot records name, size, mtime and sha256 of everything under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := de.Info()
		if err != nil {
			return err
		}
		sum := ""
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		out[p] = fmt.Sprintf("%d|%d|%s", fi.Size(), fi.ModTime().UnixNano(), sum)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The integration checks must not create, rewrite, or touch anything under HOME.
func TestIntegrationChecksAreReadOnly(t *testing.T) {
	home := t.TempDir()
	bin := fakeBin(t, home, "clio", 0o755)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	cfg := filepath.Join(home, ".claude.json")
	lease := filepath.Join(home, "mcp.lock")
	writeJSON(t, settings, settingsWith(bin+" recall", filepath.Join(home, "gone")+"/clio file-history --hook"))
	writeJSON(t, cfg, fmt.Sprintf(`{"mcpServers":{"clio":{"command":%q}}}`, bin))
	writeJSON(t, lease, fmt.Sprintf("%d 1 %d", os.Getpid(), time.Now().Add(-time.Hour).Unix()))

	d, dir, dbPath := newQuickDB(t)
	before := snapshot(t, home)
	RunWith(d, dir, dbPath, Options{Quick: true, Integration: true, SettingsPath: settings, ClaudeConfigPath: cfg, LockPath: lease})
	after := snapshot(t, home)
	if len(before) != len(after) {
		t.Fatalf("file set changed: %d -> %d entries", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s changed: %q -> %q", k, v, after[k])
		}
	}
}

// The newline probe reads in chunks; a newline past the first chunk still counts,
// and bytes before offset never do.
func TestHasNewlineAfter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	body := "x\n" + strings.Repeat("y", 200<<10)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if hasNewlineAfter(p, 2) {
		t.Fatal("no newline after offset 2")
	}
	if !hasNewlineAfter(p, 0) {
		t.Fatal("newline at 1 must be found from offset 0")
	}
	if err := os.WriteFile(p, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasNewlineAfter(p, 2) {
		t.Fatal("newline past the first chunk must be found")
	}
	if !hasNewlineAfter(filepath.Join(filepath.Dir(p), "gone"), 0) {
		t.Fatal("an unreadable file must not hide its change")
	}
}
