package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/registry"
)

// hasResult reports whether results contains a Result named name, without
// failing the test (unlike findResult, used where absence is the expected
// case).
func hasResult(results []Result, name string) (Result, bool) {
	for _, r := range results {
		if r.Name == name {
			return r, true
		}
	}
	return Result{}, false
}

// withControlledRegistrySeed points every non-default registered source's
// root resolver at deterministic temp paths, so doctor tests don't depend on
// whatever the host machine's real ~/.codex/sessions happens to contain
// (design.md D7: doctor iterates the registry instead of special-casing
// codex by name).
func withControlledRegistrySeed(t *testing.T, entries []registry.Entry) {
	t.Helper()
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = entries
}

func testDoctorDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, dir
}

// TestRunReportsNonDefaultSourceRootWhenPresent pins doctor's "codex sessions
// dir" reporting to a controlled registry seed instead of the host's real
// Codex install.
func TestRunReportsNonDefaultSourceRootWhenPresent(t *testing.T) {
	d, dir := testDoctorDB(t)
	claudeDir := filepath.Join(dir, "claude-projects")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	codexDir := filepath.Join(dir, "codex-sessions")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	withControlledRegistrySeed(t, []registry.Entry{
		{Name: "claude-code", RootLabel: "claude projects dir", RootDir: func() (string, error) { return claudeDir, nil }},
		{Name: "codex", RootLabel: "codex sessions dir", RootDir: func() (string, error) { return codexDir, nil }},
	})

	dbPath := filepath.Join(dir, "x.sqlite")
	r, ok := hasResult(Run(d, claudeDir, dbPath), "codex sessions dir")
	if !ok {
		t.Fatal(`expected a "codex sessions dir" result when the codex root is present`)
	}
	if !r.OK || r.Detail != codexDir {
		t.Errorf("codex sessions dir result = %+v, want OK=true Detail=%q", r, codexDir)
	}
}

// TestRunOmitsNonDefaultSourceRootWhenAbsent mirrors today's behavior: an
// absent non-default source root produces no result row at all (not a
// failure), because it's an optional, not-installed tool.
func TestRunOmitsNonDefaultSourceRootWhenAbsent(t *testing.T) {
	d, dir := testDoctorDB(t)
	claudeDir := filepath.Join(dir, "claude-projects")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	withControlledRegistrySeed(t, []registry.Entry{
		{Name: "claude-code", RootLabel: "claude projects dir", RootDir: func() (string, error) { return claudeDir, nil }},
		{Name: "codex", RootLabel: "codex sessions dir", RootDir: func() (string, error) { return filepath.Join(dir, "no-such-codex-dir"), nil }},
	})

	dbPath := filepath.Join(dir, "x.sqlite")
	if _, ok := hasResult(Run(d, claudeDir, dbPath), "codex sessions dir"); ok {
		t.Fatal(`expected no "codex sessions dir" result when the codex root is absent`)
	}
}

// TestRunClaudeDirAbsentButNonDefaultPresentStillOK mirrors today's
// codex-only-install behavior (claudeDirStatus), now driven by the registry
// rather than a single codexPresent bool.
func TestRunClaudeDirAbsentButNonDefaultPresentStillOK(t *testing.T) {
	d, dir := testDoctorDB(t)
	missingClaudeDir := filepath.Join(dir, "no-such-claude-projects")
	codexDir := filepath.Join(dir, "codex-sessions")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	withControlledRegistrySeed(t, []registry.Entry{
		{Name: "claude-code", RootLabel: "claude projects dir", RootDir: func() (string, error) { return missingClaudeDir, nil }},
		{Name: "codex", RootLabel: "codex sessions dir", RootDir: func() (string, error) { return codexDir, nil }},
	})

	dbPath := filepath.Join(dir, "x.sqlite")
	r := findResult(t, Run(d, missingClaudeDir, dbPath), "claude projects dir")
	if !r.OK {
		t.Errorf(`expected "claude projects dir" to be OK (codex-only install, supported), got %+v`, r)
	}
}

// TestRunReportsNonDefaultSourceRootWhenItIsARegularFile pins doctor's
// pre-registry codexPresent semantics: os.Stat succeeding is enough,
// regardless of file type. A root path that resolves to a regular file (not
// a directory) must still be reported present — mirroring the old
// `if _, serr := os.Stat(codexDir); serr == nil` check, which never tested
// IsDir (codex review P1 finding #2; contrast with bootstrap's
// TestNonDefaultSourceAvailableFalseWhenRootIsARegularFile in cli/common_test.go,
// which requires a directory).
func TestRunReportsNonDefaultSourceRootWhenItIsARegularFile(t *testing.T) {
	d, dir := testDoctorDB(t)
	claudeDir := filepath.Join(dir, "claude-projects")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	codexFile := filepath.Join(dir, "codex-sessions-is-a-file")
	if err := os.WriteFile(codexFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	withControlledRegistrySeed(t, []registry.Entry{
		{Name: "claude-code", RootLabel: "claude projects dir", RootDir: func() (string, error) { return claudeDir, nil }},
		{Name: "codex", RootLabel: "codex sessions dir", RootDir: func() (string, error) { return codexFile, nil }},
	})

	dbPath := filepath.Join(dir, "x.sqlite")
	r, ok := hasResult(Run(d, claudeDir, dbPath), "codex sessions dir")
	if !ok {
		t.Fatal(`expected a "codex sessions dir" result when the codex root exists as a regular file (doctor's Exists semantics, not IsDir)`)
	}
	if !r.OK || r.Detail != codexFile {
		t.Errorf("codex sessions dir result = %+v, want OK=true Detail=%q", r, codexFile)
	}
}

// TestRunReportsFakeThirdSourceRootWithoutDoctorEdits is the reverse-proof
// test required by tasks.md §3.1 for the doctor surface: a third registry
// entry's root is reported by name, with zero doctor.go edits.
func TestRunReportsFakeThirdSourceRootWithoutDoctorEdits(t *testing.T) {
	d, dir := testDoctorDB(t)
	claudeDir := filepath.Join(dir, "claude-projects")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeDir := filepath.Join(dir, "fake-tool-sessions")
	if err := os.MkdirAll(fakeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	withControlledRegistrySeed(t, []registry.Entry{
		{Name: "claude-code", RootLabel: "claude projects dir", RootDir: func() (string, error) { return claudeDir, nil }},
		{Name: "codex", RootLabel: "codex sessions dir", RootDir: func() (string, error) { return filepath.Join(dir, "no-codex"), nil }},
		{Name: "fake-tool", RootLabel: "fake-tool sessions dir", RootDir: func() (string, error) { return fakeDir, nil }},
	})

	dbPath := filepath.Join(dir, "x.sqlite")
	r, ok := hasResult(Run(d, claudeDir, dbPath), "fake-tool sessions dir")
	if !ok {
		t.Fatal(`expected a "fake-tool sessions dir" result for the fake registry entry`)
	}
	if !r.OK || r.Detail != fakeDir {
		t.Errorf("fake-tool sessions dir result = %+v, want OK=true Detail=%q", r, fakeDir)
	}
}
