package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/registry"
)

// TestValidateSourceGoldenThreeSourceSeed pins validateSource's accepted set and
// error text to the pre-registry hardcoded values (openspec change
// 2026-07-14-generalize-source-adapter-spi, design.md §2 golden-test gate).
func TestValidateSourceGoldenThreeSourceSeed(t *testing.T) {
	for _, ok := range []string{"", "claude-code", "codex", "gemini", "all"} {
		if err := validateSource(ok); err != nil {
			t.Errorf("validateSource(%q) = %v, want nil", ok, err)
		}
	}
	err := validateSource("bogus")
	if err == nil {
		t.Fatal("validateSource(bogus) = nil, want error")
	}
	if want := `invalid --source "bogus" (want claude-code, codex, gemini, or all)`; err.Error() != want {
		t.Errorf("validateSource(bogus) error = %q, want %q", err.Error(), want)
	}
}

// TestAddSourceFlagGoldenDefaultAndHelp pins the --source flag's default and
// help text to the pre-registry hardcoded values.
func TestAddSourceFlagGoldenDefaultAndHelp(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	var source string
	addSourceFlag(cmd, &source)
	f := cmd.Flags().Lookup("source")
	if f == nil {
		t.Fatal("expected --source flag to be registered")
	}
	if f.DefValue != "claude-code" {
		t.Errorf("--source default = %q, want %q", f.DefValue, "claude-code")
	}
	if want := "Which tool's history: claude-code | codex | gemini | all"; f.Usage != want {
		t.Errorf("--source usage = %q, want %q", f.Usage, want)
	}
}

// TestValidateSourceAndFlagTrackFakeSeedEntry proves both derive from the
// registry rather than a hardcoded literal list (tasks.md §3.1): extending
// registry.Seed makes the fake name valid and shows up in help text, without
// editing common.go.
func TestValidateSourceAndFlagTrackFakeSeedEntry(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = append(append([]registry.Entry{}, orig...), registry.Entry{
		Name:    "fake-tool",
		RootDir: func() (string, error) { return "/nonexistent/fake-tool", nil },
	})

	if err := validateSource("fake-tool"); err != nil {
		t.Errorf("validateSource(fake-tool) = %v, want nil after seeding fake-tool", err)
	}

	cmd := &cobra.Command{Use: "x"}
	var source string
	addSourceFlag(cmd, &source)
	f := cmd.Flags().Lookup("source")
	if want := "Which tool's history: claude-code | codex | gemini | fake-tool | all"; f.Usage != want {
		t.Errorf("--source usage with fake entry = %q, want %q", f.Usage, want)
	}
}

// TestNonDefaultSourceAvailableGoldenTwoSourceSeed exercises the bootstrap
// helper (generalized from codexAvailable) against a Seed whose roots are
// redirected to temp dirs, so it doesn't depend on the host's real
// ~/.codex/sessions.
func TestNonDefaultSourceAvailableGoldenTwoSourceSeed(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	tmp := t.TempDir()
	registry.Seed = []registry.Entry{
		{Name: "claude-code", RootDir: func() (string, error) { return "/nonexistent/claude", nil }},
		{Name: "codex", RootDir: func() (string, error) { return tmp, nil }},
	}
	if !nonDefaultSourceAvailable() {
		t.Error("nonDefaultSourceAvailable() = false, want true when the codex root exists")
	}

	registry.Seed = []registry.Entry{
		{Name: "claude-code", RootDir: func() (string, error) { return "/nonexistent/claude", nil }},
		{Name: "codex", RootDir: func() (string, error) { return "/nonexistent/codex", nil }},
	}
	if nonDefaultSourceAvailable() {
		t.Error("nonDefaultSourceAvailable() = true, want false when no non-default root exists")
	}
}

// TestNonDefaultSourceAvailableFalseWhenRootIsARegularFile pins bootstrap's
// pre-registry directory requirement (codexAvailable's fi.IsDir()): a root
// path that resolves to a regular file, not a directory, must not count as
// available (codex review P1 finding #2; contrast with doctor's
// TestRunReportsNonDefaultSourceRootWhenItIsARegularFile in
// internal/doctor/registry_test.go, which reports the same path present).
func TestNonDefaultSourceAvailableFalseWhenRootIsARegularFile(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry.Seed = []registry.Entry{
		{Name: "claude-code", RootDir: func() (string, error) { return "/nonexistent/claude", nil }},
		{Name: "codex", RootDir: func() (string, error) { return filePath, nil }},
	}
	if nonDefaultSourceAvailable() {
		t.Error("nonDefaultSourceAvailable() = true, want false when the non-default root is a regular file, not a directory")
	}
}

// TestBootstrapMissingSourcesErrorGoldenThreeSourceSeed pins
// bootstrapMissingSourcesError's message to the pre-registry hardcoded
// literal ("neither %s nor a Codex sessions dir exists: %w"), proving the
// refactor to generate the non-default clause from the registry doesn't
// change today's output (golden-test gate).
func TestBootstrapMissingSourcesErrorGoldenThreeSourceSeed(t *testing.T) {
	statErr := errors.New("stat failed")
	err := bootstrapMissingSourcesError("/x/projects", statErr)
	want := `no sessions to index: neither /x/projects nor a Codex sessions dir, or a Gemini chats dir exists: stat failed`
	if err.Error() != want {
		t.Errorf("bootstrapMissingSourcesError() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, statErr) {
		t.Error("bootstrapMissingSourcesError() must wrap the original stat error")
	}
}

// TestBootstrapMissingSourcesErrorTracksFakeSeedEntry proves the non-default
// clause is generated from the registry, not a hardcoded "Codex" literal
// (codex review P1 finding #1): extending registry.Seed makes the fake
// source's label appear in the message without editing index.go/
// install_mcp.go.
func TestBootstrapMissingSourcesErrorTracksFakeSeedEntry(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = append(append([]registry.Entry{}, orig...), registry.Entry{
		Name:      "fake-tool",
		RootLabel: "fake-tool sessions dir",
		RootDir:   func() (string, error) { return "/nonexistent/fake-tool", nil },
	})

	err := bootstrapMissingSourcesError("/x/projects", errors.New("stat failed"))
	if !strings.Contains(strings.ToLower(err.Error()), "fake-tool sessions dir") {
		t.Errorf("bootstrapMissingSourcesError() = %q, want it to name the fake-tool sessions dir", err.Error())
	}
}

func TestOpenForQueryDefersToLiveLeader(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if _, err := config.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	dbPath, err := config.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	// Seed the index so openAndCatchUp doesn't error on "no index".
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()

	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	lease, isLeader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !isLeader {
		t.Fatalf("expected to lead: leader=%v err=%v", isLeader, err)
	}
	defer lease.Release()

	if !lock.IsHeld(lockPath) {
		t.Fatal("lock should read as held while a live leader exists")
	}
	// openAndCatchUp should defer to the leader and return a usable RO handle.
	d, err := openAndCatchUp()
	if err != nil {
		t.Fatalf("openAndCatchUp: %v", err)
	}
	defer d.Close()
}
