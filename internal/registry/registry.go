// Package registry is clio's source registry: the single source of truth
// for the set of registered source names, their display labels, and their
// root-directory availability. Every surface that used to enumerate or
// branch on source names — the CLI --source flag, the CLI bootstrap check,
// the MCP read tools' source enum, doctor's per-source report, the TUI
// source labels, and the DB source-filter default — derives its values from
// this package instead of hardcoding {claude-code, codex, all} or a
// per-source-name branch.
//
// Shape: a static-seeded slice (Seed) plus small helpers. There is no
// dynamic registration API — adding a source means adding one entry to Seed
// at compile time (design.md §2). Seed is exported only so tests in other
// packages can extend it with a fake entry to prove derivation works without
// editing surface code (tasks.md §3.1); production code must never mutate
// it, and no production code path does.
package registry

import (
	"os"
	"strings"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/model"
)

// All is the pseudo-value meaning "no source filter" — owned by these
// derivation helpers, not a registry entry (design.md §2).
const All = "all"

// Entry is one registered source.
type Entry struct {
	// Name is the source's identifier, e.g. "claude-code" or "codex".
	Name string
	// Label is the display-label prefix used by the TUI for non-default
	// sources, e.g. "[codex]". Empty means no label (the default source).
	Label string
	// RootLabel is the human-readable name doctor uses when reporting this
	// source's root directory, e.g. "codex sessions dir".
	RootLabel string
	// RootDir resolves this source's root directory. It errors the same way
	// its underlying config resolver does (e.g. no home directory).
	RootDir func() (string, error)
}

// Seed is the compile-time-static source list, seeded from the model
// package's source-name constants (design.md D1). The first entry is the
// default/fallback source. Exported for the test seam described above;
// production code must treat it as read-only.
//
// Mutation timing matters: some derivation callers (e.g. mcp.NewServer,
// cli.addSourceFlag) read Seed-derived values once at construction time and
// bake them into a longer-lived object (a tool's schema, a cobra flag's
// Usage/DefValue), while others (e.g. MCP handler defaults via
// registry.DefaultSource) re-derive at request time. Mutating Seed after
// construction therefore does NOT no-op — it desynchronizes the baked
// snapshots from the live derivations (advertised schema vs handler
// behavior). Seed must stay unchanged for the lifetime of any constructed
// object: a test that mutates it must do so *before* constructing whatever
// it's testing and restore via t.Cleanup. (See the orig-Seed/t.Cleanup
// pattern used throughout registry_test.go and the surface tests in
// cli/mcp/doctor/tui/db.)
var Seed = []Entry{
	{
		Name:      model.SourceClaudeCode,
		Label:     "",
		RootLabel: "claude projects dir",
		RootDir:   config.ClaudeProjectsDir,
	},
	{
		Name:      model.SourceCodex,
		Label:     "[codex]",
		RootLabel: "codex sessions dir",
		RootDir:   config.CodexSessionsDir,
	},
}

// Names returns the registered source names, in Seed order.
func Names() []string {
	out := make([]string, len(Seed))
	for i, e := range Seed {
		out[i] = e.Name
	}
	return out
}

// EnumValues returns the accepted --source / MCP source enum values: every
// registered name plus All, in Seed order.
func EnumValues() []string {
	return append(Names(), All)
}

// DefaultSource is the fallback source used when --source/source is omitted:
// the first entry in Seed.
func DefaultSource() string {
	if len(Seed) == 0 {
		return ""
	}
	return Seed[0].Name
}

// IsValid reports whether name is a registered source name. All is not a
// registered source — it is a filter-only pseudo-value — so IsValid("all")
// is false; callers that also accept "all" (and "") check for it separately.
func IsValid(name string) bool {
	for _, e := range Seed {
		if e.Name == name {
			return true
		}
	}
	return false
}

// Label returns the display-label prefix for a source (e.g. "[codex]"), or
// "" for the default source or an unregistered/empty name.
func Label(name string) string {
	for _, e := range Seed {
		if e.Name == name {
			return e.Label
		}
	}
	return ""
}

// RootStatus is one registered source's resolved root directory and its
// presence on disk.
//
// Exists and IsDir are deliberately two separate booleans, not one collapsed
// "Present": doctor's pre-registry semantics (codexPresent) considered a root
// present whenever os.Stat succeeded, regardless of file type, while CLI
// bootstrap's pre-registry semantics (codexAvailable) additionally required
// fi.IsDir(). Collapsing them into one field would silently change one of
// the two callers' behavior for a root path that exists but isn't a
// directory (codex review, tasks.md finding #2). Callers pick the field that
// matches their own pre-registry semantics: doctor uses Exists, bootstrap
// (NonDefaultRootAvailable) uses IsDir.
type RootStatus struct {
	Name  string
	Label string
	Dir   string
	// Exists reports whether the root path exists on disk (os.Stat
	// succeeded), regardless of whether it's a directory. This is doctor's
	// "is this source's root there at all" semantics.
	Exists bool
	// IsDir reports whether the root path exists AND is a directory. This is
	// CLI bootstrap's "can I actually treat this as a session root"
	// semantics.
	IsDir     bool
	IsDefault bool
	Err       error // non-nil if the root resolver itself failed
}

// Roots resolves and stats every registered source's root directory, in
// Seed order.
func Roots() []RootStatus {
	def := DefaultSource()
	out := make([]RootStatus, 0, len(Seed))
	for _, e := range Seed {
		rs := RootStatus{Name: e.Name, Label: e.RootLabel, IsDefault: e.Name == def}
		dir, err := e.RootDir()
		if err != nil {
			rs.Err = err
			out = append(out, rs)
			continue
		}
		rs.Dir = dir
		fi, statErr := os.Stat(dir)
		rs.Exists = statErr == nil
		rs.IsDir = statErr == nil && fi.IsDir()
		out = append(out, rs)
	}
	return out
}

// NonDefaultRootAvailable reports whether any registered source other than
// the default (claude-code) has an available root directory. Used by CLI
// bootstrap to let a machine with only a non-Claude source still proceed
// (design.md D4). Uses IsDir (not Exists): bootstrap's pre-registry
// semantics (codexAvailable) required the root be a directory.
func NonDefaultRootAvailable() bool {
	for _, rs := range Roots() {
		if !rs.IsDefault && rs.IsDir {
			return true
		}
	}
	return false
}

// capitalize upper-cases s's first rune, leaving the rest unchanged. Used to
// turn a lowercase RootLabel (e.g. "codex sessions dir") into the
// mid-sentence-noun-phrase form used in prose like "a Codex sessions dir".
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// NonDefaultRootLabelsProse renders every registered non-default source's
// root label as an "a X[, a Y, ...] or a Z" clause (each label capitalized),
// for bootstrap-style error messages that name which optional root(s) were
// looked for besides the default source's own dir. Empty if there are no
// non-default registered sources. Derived from the registry rather than a
// hardcoded "a Codex sessions dir" literal, so an additional registered
// source is named here without editing the caller (codex review, tasks.md
// finding #1).
func NonDefaultRootLabelsProse() string {
	def := DefaultSource()
	var labels []string
	for _, e := range Seed {
		if e.Name == def {
			continue
		}
		labels = append(labels, "a "+capitalize(e.RootLabel))
	}
	switch len(labels) {
	case 0:
		return ""
	case 1:
		return labels[0]
	default:
		return strings.Join(labels[:len(labels)-1], ", ") + ", or " + labels[len(labels)-1]
	}
}
