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
	t.Run("legacy unquoted path with spaces resolves to the whole path", func(t *testing.T) {
		sp := filepath.Join(dir, "c d")
		if err := os.MkdirAll(sp, 0o755); err != nil {
			t.Fatal(err)
		}
		bin := fakeBin(t, sp, "clio", 0o755)
		writeJSON(t, settings, settingsWith(bin+" recall", bin+" file-history --hook"))
		if r := checkHookBinary(settings); !r.OK || !strings.Contains(r.Detail, bin) {
			t.Fatalf("got %+v", r)
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

func TestIndexFreshnessMeasuresPipelineNotLastIngest(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()

	if r := checkIndexFreshness(d, now); r.OK || !strings.Contains(r.Detail, "never indexed") {
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

	// 1. A fully absorbed file whose last ingest was 3 days ago: the user simply did
	// not use clio. Must be ok — `clio index` cannot (and need not) refresh this.
	p1, fi1 := mk("absorbed.jsonl", "0123456789", 72*time.Hour)
	ingestRow(t, d, p1, fi1.Size(), fi1.Size(), fi1.ModTime().UnixNano(), now.Add(-72*time.Hour).Unix())
	r := checkIndexFreshness(d, now)
	if !r.OK || !strings.Contains(r.Detail, "up to date (last change 3d ago)") {
		t.Fatalf("quiet-but-complete index must be ok: %+v", r)
	}

	// 2. New bytes appended 1h ago and not yet absorbed: within the grace window.
	p2, fi2 := mk("recent.jsonl", "0123456789ABCDEF", 1*time.Hour)
	ingestRow(t, d, p2, 10, 10, fi2.ModTime().UnixNano()-1, now.Add(-2*time.Hour).Unix())
	if r := checkIndexFreshness(d, now); !r.OK {
		t.Fatalf("unabsorbed bytes under 24h old must still be ok: %+v", r)
	}

	// 3. A file with new bytes whose mtime is 30h old and which was never absorbed.
	p3, fi3 := mk("behind.jsonl", "0123456789ABCDEF", 30*time.Hour)
	ingestRow(t, d, p3, 10, 10, fi3.ModTime().UnixNano()-1, now.Add(-72*time.Hour).Unix())
	r = checkIndexFreshness(d, now)
	if r.OK || !strings.Contains(r.Detail, "index behind source by 30h; run clio index") {
		t.Fatalf("index behind source by 30h must warn: %+v", r)
	}
	_ = fi3

	// 4. A same-size rewrite (mtime moved, size equal) 40h ago also counts as behind.
	p4, fi4 := mk("rewritten.jsonl", "0123456789", 40*time.Hour)
	ingestRow(t, d, p4, fi4.Size(), fi4.Size(), fi4.ModTime().UnixNano()-5, now.Add(-72*time.Hour).Unix())
	r = checkIndexFreshness(d, now)
	if r.OK || !strings.Contains(r.Detail, "behind source by 40h") {
		t.Fatalf("worst lag (40h) must be reported: %+v", r)
	}

	// 5. A tracked file that no longer exists is not "behind" (reconciliation owns that).
	d.Exec(`DELETE FROM ingest_state WHERE source_file IN (?,?)`, p3, p4)
	ingestRow(t, d, filepath.Join(dir, "gone.jsonl"), 10, 10, 1, now.Add(-72*time.Hour).Unix())
	if r := checkIndexFreshness(d, now); !r.OK {
		t.Fatalf("missing tracked file must not trip freshness: %+v", r)
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
