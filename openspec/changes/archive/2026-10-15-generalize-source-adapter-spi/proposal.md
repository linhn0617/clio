## Why

clio already has a source-adapter SPI (`internal/ingest/source.go`, added by
`2026-06-19-cross-tool-ingestion`) with two adapters, `claude-code` and `codex`. A codex
(GPT) review of the "add a provider = add an adapter" assumption found two distinct
problems:

1. **Identity duplication (this change).** The source-name literal set
   `{claude-code, codex, all}` and per-source branches are hardcoded across ~10 surfaces
   (grep-verified inventory in `design.md` §1): the CLI `--source` default + validation
   (`internal/cli/common.go:26,31`), the codex-specific bootstrap branch `codexAvailable`
   (`internal/cli/common.go:41`, called at `internal/cli/index.go:35` and
   `internal/cli/install_mcp.go:29`), five MCP tool enums (`internal/mcp/server.go:39,52,
   69,82,96`) and their five defaults (`internal/mcp/tools.go`), the `doctor` codex
   special-case (`internal/doctor/doctor.go:38`), the DB source filter's default-source
   knowledge (`internal/db/db.go:182-191`), and two TUI label literals
   (`internal/tui/browse_view.go:249`, `internal/tui/search_view.go:229`). Adding any
   third source means hand-editing every one, and missing one fails silently.

2. **Structural SPI assumptions (deferred, see below).** Discovery is `.jsonl`-only,
   incremental state is a byte offset, and one file = one session — real blockers for a
   SQLite-shaped (Cursor) or Markdown-shaped (Aider) source, but generalizing them now
   would mean designing seams against zero concrete non-JSONL instances.

This change delivers only #1: a lightweight **source registry** as the single source of
truth for source names/labels, with every surface above derived from it.

## What Changes

- **Added** a lightweight source registry — a static-seeded list plus helpers (it can be a
  slice; no dynamic registration mechanism) — exposing each source's name, display label,
  and root availability. Seeded at compile time from the existing `model` constants
  (`internal/model/model.go:8-9`).
- **Modified** every source-enumerating surface to derive from the registry instead of
  hardcoding `{claude-code, codex, all}` or branching on a source name: CLI `--source`
  validation/help, the CLI bootstrap check (generalizing `codexAvailable` to "any
  registered non-Claude source root is available"), the five MCP `source` enums and
  defaults, `doctor`'s per-source reporting, the TUI source labels, and the DB
  source-filter default.
- **Golden-test gate:** with only `claude-code` and `codex` registered, every derived
  value set is **string-identical** to today's hardcoded values, so behavior is unchanged.

### Deferred (to the first real provider's change)

The structural SPI generalizations are explicitly **not** delivered here; recorded design
drafts live in `design.md` §3. Rationale: rule-of-three — the two existing adapters are the
same shape (append-only JSONL), and two same-shape instances are not enough evidence to fix
the seams correctly. Deferred items:

- Adapter-declared discovery (removing the `.jsonl` filter from `WalkSessionFiles` and its
  four non-test callers).
- Opaque incremental cursor (+ any `ingest_state` migration), including `doctor`'s
  byte-offset-coupled `reconcile`.
- Multiple sessions per source file.
- Capability flags (`IsFallback` / `SupportsIncremental` / `ExtractsActivity`) — zero
  consumers exist in the tree today; fallback ownership is already served by the
  registration-order convention (`internal/ingest/ingest.go:68`).

## Non-goals

- No new concrete provider ships. First-provider candidates are recorded side by side in
  `design.md` §4 **without an ordering commitment**; the decision belongs to that
  implementation change, made against real format docs.
- No change to ingest semantics, incremental behavior, schema, or the `Source` interface.

## Capabilities

### Modified Capabilities

- `session-ingest`: adds the source-registry requirement (single source of truth for
  source names/labels; surfaces derive from it).
- `cli-surface`: `--source` values/validation and the bootstrap source-availability check
  derived from the registry.
- `mcp-server`: the five read tools' `source` enum + default derived from the registry.
- `tui`: source labels derived from the registry, not a hardcoded `codex` literal.
- `diagnostics`: `doctor` iterates the registry's sources instead of special-casing codex.
