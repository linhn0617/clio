## 1. Source registry (`internal/model` or a small new package)

- [ ] 1.1 Build the registry as a static-seeded slice + helpers — `Names()`,
  `Label(name)`, `IsValid(name)`, default-source accessor, per-source root availability —
  seeded at compile time from `model.SourceClaudeCode` / `model.SourceCodex`
  (`internal/model/model.go:8-9`). No dynamic registration API (TDD).
  *Acceptance:* helper outputs for the two-source seed are exactly
  `{claude-code, codex}` (+ `all` at the filter layer), default `claude-code`, label
  `[codex]` for codex and none for claude-code.

## 2. Derive every surface from the registry (delete the hardcoded literals)

- [ ] 2.1 CLI: `validateSource` + `--source` help/default (`internal/cli/common.go:26,
  31-36`) derive from the registry (TDD).
- [ ] 2.2 CLI bootstrap: generalize `codexAvailable` (`internal/cli/common.go:39-48`) to
  "any registered non-Claude source root is available", updating both callers
  (`internal/cli/index.go:35`, `internal/cli/install_mcp.go:29`) so a machine with only a
  non-Claude source still bootstraps (TDD).
- [ ] 2.3 MCP: one helper feeds the five tools' `mcp.Enum` values + `DefaultString`
  (`internal/mcp/server.go:39,52,69,82,96`) and the five handler defaults
  (`internal/mcp/tools.go:59,96,131,176,255`) from the registry (TDD; `mcp.Enum` is
  variadic, so passing a derived `[]string...` is compatible).
- [ ] 2.4 doctor: iterate registry sources for root presence/health instead of the codex
  special-case (`internal/doctor/doctor.go:38-62`); per-source output equivalent to today
  (TDD). (Note: `reconcile`'s byte-offset logic at `doctor.go:263,288-297` is untouched —
  it belongs to the deferred cursor work, `design.md` §3b.)
- [ ] 2.5 TUI: row labels via `registry.Label` (`internal/tui/browse_view.go:249`,
  `internal/tui/search_view.go:229`) (TDD).
- [ ] 2.6 DB: the source-filter default in `internal/db/db.go:182-191` takes the default
  source name from the registry (constants stay the values) (TDD).

## 3. Golden-test gate (behavior unchanged)

- [ ] 3.1 Golden tests assert string-identity with the pre-change hardcoded values, with
  only `claude-code`+`codex` seeded: the accepted CLI `--source` set (incl. `""`), the
  CLI help text, all five MCP enums + defaults, the TUI labels, the bootstrap behavior
  (no `~/.claude/projects` + existing codex root still proceeds), and `doctor`'s
  per-source lines.
  *Acceptance:* all golden tests green; a follow-up test adds a fake entry to the seed
  and asserts it appears in every derived surface **without editing any surface code** —
  the objective proof the duplication is gone.

## 4. Verify

- [ ] 4.1 `go build/vet/test ./...` green (incl. `-race` and windows cross-build);
  `gofmt -l .` clean; `openspec validate --strict`; smoke-test read-only against a
  **copy** of the real index (absolute `XDG_DATA_HOME` redirect) — live db untouched.
- [ ] 4.2 Third-party (codex) review of the real diff to a clean gate — specifically that
  no surface retains a source-name literal list or `== "codex"` branch (grep gate:
  `mcp.Enum("claude-code"`, `"claude-code", "codex", "all"`, `== "codex"` yield no
  non-test hits outside the registry seed), and that ingest semantics are untouched —
  re-review after every fix; then Claude `/review`.
