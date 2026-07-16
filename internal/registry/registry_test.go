package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/linhn0617/clio/internal/model"
)

// TestNamesMatchesTwoSourceSeed pins the registry's helper outputs for the
// current two-source seed to the pre-registry hardcoded values (tasks.md
// §1.1 acceptance).
func TestNamesMatchesTwoSourceSeed(t *testing.T) {
	got := Names()
	want := []string{model.SourceClaudeCode, model.SourceCodex}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestEnumValuesAppendsAll(t *testing.T) {
	got := EnumValues()
	want := []string{model.SourceClaudeCode, model.SourceCodex, All}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnumValues() = %v, want %v", got, want)
	}
}

func TestDefaultSourceIsClaudeCode(t *testing.T) {
	if got := DefaultSource(); got != model.SourceClaudeCode {
		t.Fatalf("DefaultSource() = %q, want %q", got, model.SourceClaudeCode)
	}
}

func TestIsValid(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{model.SourceClaudeCode, true},
		{model.SourceCodex, true},
		{"all", false}, // "all" is a filter-only pseudo-value, not a registered source
		{"", false},
		{"bogus", false},
	}
	for _, c := range cases {
		if got := IsValid(c.name); got != c.want {
			t.Errorf("IsValid(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLabel(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{model.SourceCodex, "[codex]"},
		{model.SourceClaudeCode, ""},
		{"", ""},
		{"bogus", ""},
	}
	for _, c := range cases {
		if got := Label(c.name); got != c.want {
			t.Errorf("Label(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRootsReflectsSeedOrderAndDefault pins Roots()'s structural output
// (name/label/IsDefault) independent of the current machine's actual
// filesystem state (Present/Dir/Err vary by host).
func TestRootsReflectsSeedOrderAndDefault(t *testing.T) {
	roots := Roots()
	if len(roots) != 2 {
		t.Fatalf("Roots() len = %d, want 2", len(roots))
	}
	if roots[0].Name != model.SourceClaudeCode || !roots[0].IsDefault {
		t.Errorf("Roots()[0] = %+v, want claude-code/IsDefault=true", roots[0])
	}
	if roots[1].Name != model.SourceCodex || roots[1].IsDefault {
		t.Errorf("Roots()[1] = %+v, want codex/IsDefault=false", roots[1])
	}
	if roots[1].Label != "codex sessions dir" {
		t.Errorf("Roots()[1].Label = %q, want %q", roots[1].Label, "codex sessions dir")
	}
	if roots[0].Label != "claude projects dir" {
		t.Errorf("Roots()[0].Label = %q, want %q", roots[0].Label, "claude projects dir")
	}
}

// TestFakeSeedEntryAppearsInHelpersWithoutCodeEdits is the reverse-proof test
// required by tasks.md §3.1: extending Seed with a fake entry must make it
// show up in every helper without editing this package's non-test code
// (Names/EnumValues/IsValid/Label/Roots all read Seed directly, not a
// baked-in literal). Seed is restored so the fake entry never leaks into
// other tests in this package or process.
func TestFakeSeedEntryAppearsInHelpersWithoutCodeEdits(t *testing.T) {
	orig := Seed
	t.Cleanup(func() { Seed = orig })

	fakeRootCalled := false
	Seed = append(append([]Entry{}, orig...), Entry{
		Name:      "fake-tool",
		Label:     "[fake-tool]",
		RootLabel: "fake-tool sessions dir",
		RootDir: func() (string, error) {
			fakeRootCalled = true
			return "/nonexistent/fake-tool", nil
		},
	})

	if got, want := Names(), []string{model.SourceClaudeCode, model.SourceCodex, "fake-tool"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() with fake entry = %v, want %v", got, want)
	}
	if got, want := EnumValues(), []string{model.SourceClaudeCode, model.SourceCodex, "fake-tool", All}; !reflect.DeepEqual(got, want) {
		t.Errorf("EnumValues() with fake entry = %v, want %v", got, want)
	}
	if !IsValid("fake-tool") {
		t.Error("IsValid(fake-tool) = false, want true")
	}
	if got := Label("fake-tool"); got != "[fake-tool]" {
		t.Errorf("Label(fake-tool) = %q, want [fake-tool]", got)
	}
	// DefaultSource must be unaffected: the fake entry was appended, not
	// prepended, so claude-code is still Seed[0].
	if got := DefaultSource(); got != model.SourceClaudeCode {
		t.Errorf("DefaultSource() with fake entry = %q, want %q", got, model.SourceClaudeCode)
	}

	roots := Roots()
	if len(roots) != 3 {
		t.Fatalf("Roots() with fake entry len = %d, want 3", len(roots))
	}
	fake := roots[2]
	if fake.Name != "fake-tool" || fake.IsDefault || fake.Exists || fake.IsDir {
		t.Errorf("Roots()[2] = %+v, want fake-tool/IsDefault=false/Exists=false/IsDir=false", fake)
	}
	if !fakeRootCalled {
		t.Error("Roots() did not call the fake entry's RootDir resolver")
	}
}

func TestNonDefaultRootAvailableFalseWhenNeitherOtherRootExists(t *testing.T) {
	orig := Seed
	t.Cleanup(func() { Seed = orig })
	Seed = []Entry{
		{Name: model.SourceClaudeCode, RootDir: func() (string, error) { return "/nonexistent/claude", nil }},
		{Name: model.SourceCodex, RootDir: func() (string, error) { return "/nonexistent/codex", nil }},
	}
	if NonDefaultRootAvailable() {
		t.Error("NonDefaultRootAvailable() = true, want false when no non-default root exists")
	}
}

func TestNonDefaultRootAvailableTrueWhenNonDefaultRootExists(t *testing.T) {
	orig := Seed
	t.Cleanup(func() { Seed = orig })
	tmp := t.TempDir()
	Seed = []Entry{
		{Name: model.SourceClaudeCode, RootDir: func() (string, error) { return "/nonexistent/claude", nil }},
		{Name: model.SourceCodex, RootDir: func() (string, error) { return tmp, nil }},
	}
	if !NonDefaultRootAvailable() {
		t.Error("NonDefaultRootAvailable() = false, want true when a non-default root exists")
	}
}

// TestRootsExistsVsIsDirForARegularFile pins the two intentionally different
// existence semantics RootStatus must expose (codex review P1 finding #2,
// design.md D4/D7). Pre-registry: doctor's codexPresent considered a root
// "present" whenever os.Stat succeeded, regardless of file type (any type
// counts); bootstrap's codexAvailable additionally required fi.IsDir(). A
// root path that resolves to a regular file (not a directory) must therefore
// report Exists=true (doctor's semantics) but IsDir=false (bootstrap's
// semantics) — collapsing both into one bool would silently change one of
// the two callers' behavior.
func TestRootsExistsVsIsDirForARegularFile(t *testing.T) {
	orig := Seed
	t.Cleanup(func() { Seed = orig })
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	Seed = []Entry{
		{Name: model.SourceClaudeCode, RootDir: func() (string, error) { return "/nonexistent/claude", nil }},
		{Name: model.SourceCodex, RootDir: func() (string, error) { return filePath, nil }},
	}

	roots := Roots()
	codex := roots[1]
	if !codex.Exists {
		t.Error("Roots()[1].Exists = false, want true for a regular file at the root path (os.Stat succeeded)")
	}
	if codex.IsDir {
		t.Error("Roots()[1].IsDir = true, want false for a regular file at the root path")
	}
}

// TestNonDefaultRootAvailableFalseWhenNonDefaultRootIsARegularFile pins
// bootstrap's directory requirement: a regular file at the root path must
// not count as available, mirroring the pre-registry codexAvailable's
// fi.IsDir() check (codex review P1 finding #2).
func TestNonDefaultRootAvailableFalseWhenNonDefaultRootIsARegularFile(t *testing.T) {
	orig := Seed
	t.Cleanup(func() { Seed = orig })
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	Seed = []Entry{
		{Name: model.SourceClaudeCode, RootDir: func() (string, error) { return "/nonexistent/claude", nil }},
		{Name: model.SourceCodex, RootDir: func() (string, error) { return filePath, nil }},
	}

	if NonDefaultRootAvailable() {
		t.Error("NonDefaultRootAvailable() = true, want false when the non-default root is a regular file, not a directory")
	}
}

func TestNonDefaultRootAvailableIgnoresDefaultSourceRoot(t *testing.T) {
	orig := Seed
	t.Cleanup(func() { Seed = orig })
	tmp := t.TempDir()
	Seed = []Entry{
		// Only the default source's root exists; NonDefaultRootAvailable must
		// still report false — it answers "is any *other* source's root
		// available", not "is any root available".
		{Name: model.SourceClaudeCode, RootDir: func() (string, error) { return tmp, nil }},
		{Name: model.SourceCodex, RootDir: func() (string, error) { return "/nonexistent/codex", nil }},
	}
	if NonDefaultRootAvailable() {
		t.Error("NonDefaultRootAvailable() = true, want false when only the default source's root exists")
	}
}
