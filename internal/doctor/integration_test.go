package doctor

import (
	"fmt"
	"os"
	"path/filepath"
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
	t.Run("malformed settings warns instead of crashing", func(t *testing.T) {
		writeJSON(t, settings, `{oops`)
		if r := checkHookBinary(settings); r.OK {
			t.Fatalf("got %+v", r)
		}
	})
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
	t.Run("malformed config warns", func(t *testing.T) {
		writeJSON(t, cfg, `{oops`)
		if r := checkMCPRegistration(cfg); r.OK {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestLeaseCheck(t *testing.T) {
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

	// Stale: same format on disk, heartbeat far in the past.
	old := time.Now().Add(-5 * time.Minute).Unix()
	writeJSON(t, path, fmt.Sprintf("%d 42 %d", os.Getpid(), old))
	r := checkLease(path)
	if r.OK || !strings.Contains(r.Detail, "stale lease (last heartbeat") {
		t.Fatalf("stale lease must warn: %+v", r)
	}

	writeJSON(t, path, "garbage")
	if r := checkLease(path); r.OK {
		t.Fatalf("malformed lease must warn: %+v", r)
	}
}

func TestIndexFreshnessCheck(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Unix(2_000_000_000, 0)

	if r := checkIndexFreshness(d, now); r.OK || !strings.Contains(r.Detail, "never indexed") {
		t.Fatalf("empty ingest_state: %+v", r)
	}

	ins := func(ts int64) {
		t.Helper()
		if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("f%d", ts), 1, 1, 1, "fp", ts); err != nil {
			t.Fatal(err)
		}
	}
	ins(now.Unix() - 48*3600)
	r := checkIndexFreshness(d, now)
	if r.OK || !strings.Contains(r.Detail, "index last updated") || !strings.Contains(r.Detail, "run clio index") {
		t.Fatalf("48h-old index must warn: %+v", r)
	}

	// MAX wins: one recent row makes the index fresh even with older rows around.
	ins(now.Unix() - 3600)
	if r := checkIndexFreshness(d, now); !r.OK {
		t.Fatalf("1h-old max must be ok: %+v", r)
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

func TestQuickModeSkipsHeavyChecks(t *testing.T) {
	d, dir, dbPath := newQuickDB(t)

	full := RunWith(d, dir, dbPath, Options{})
	for _, name := range []string{"integrity_check", "fts content check", "fts bloat"} {
		for _, r := range full {
			if r.Name == name && r.Skipped {
				t.Fatalf("full run must not skip %q", name)
			}
		}
	}
	if r := findResult(t, full, "fts bloat"); r.Skipped {
		t.Fatalf("full run reports fts bloat: %+v", r)
	}

	quick := RunWith(d, dir, dbPath, Options{Quick: true})
	for _, name := range []string{"integrity_check", "fts content check", "fts bloat"} {
		r := findResult(t, quick, name)
		if !r.Skipped || !r.OK || !strings.Contains(r.Detail, "--quick") {
			t.Fatalf("%q must be a skip mentioning --quick, got %+v", name, r)
		}
	}
	if r := findResult(t, quick, "db integrity"); !r.OK || !strings.Contains(r.Detail, "quick_check") {
		t.Fatalf("quick mode must run quick_check, got %+v", r)
	}
	// The cheap row-count comparison still runs.
	if r := findResult(t, quick, "fts index"); !r.OK || strings.Contains(r.Detail, "content verified") {
		t.Fatalf("fts index in quick mode: %+v", r)
	}
}

// Quick mode must not open the writable FTS connection nor query dbstat:
// proving it by pointing dbPath at a non-database. The full run would report
// an fts failure here; quick mode never touches that path for the content check.
func TestQuickModeDoesNotOpenWritableConnection(t *testing.T) {
	d, dir, _ := newQuickDB(t)
	r := findResult(t, RunWith(d, dir, filepath.Join(dir, "does-not-exist.sqlite"), Options{Quick: true}), "fts index")
	if !r.OK {
		t.Fatalf("quick fts index must not need a writable connection: %+v", r)
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
			findResult(t, res, name)
		}
	}
	// Plain Run (legacy callers/tests) stays hermetic: no HOME-dependent checks.
	for _, r := range Run(d, dir, dbPath) {
		if r.Name == "hook binary" || r.Name == "mcp registration" {
			t.Fatalf("Run must not read the real HOME, got %q", r.Name)
		}
	}
}
