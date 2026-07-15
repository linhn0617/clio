# Design: source registry + de-hardcoding source identity

Scope note (post-adversarial-review, 2026-07-14): this change delivers **only** the
source registry and the derivation of every source-enumerating surface from it. The
structural SPI generalizations (discovery, cursor, multi-session, capability flags) are
**deferred** — recorded as drafts in §3, delivered by the first real provider's change.
Rationale: rule-of-three. Both existing adapters are the same shape (append-only JSONL,
one session per file, id in filename); designing generalized seams against two same-shape
instances risks fixing the wrong abstraction, which is costlier to undo than adding it
later against a concrete third shape.

## 1. Current-state inventory — per-source hardcoding this change removes

Every row grep-verified against the working tree (2026-07-14).

| # | Location | Hardcoded literal / branch | Registry-derived replacement |
|---|---|---|---|
| D1 | `internal/model/model.go:8-9` | `SourceClaudeCode = "claude-code"`, `SourceCodex = "codex"` | **Kept** — these constants are the registry's static seed |
| D2 | `internal/cli/common.go:26` | `--source` default `"claude-code"` + help text `claude-code \| codex \| all` | Help/enum text built from registry names + `all`; default = registry default source |
| D3 | `internal/cli/common.go:31-36` (`validateSource`) | `switch { case "", "claude-code", "codex", "all" }` | Accept registry names + `all`; reject the rest |
| D4 | `internal/cli/common.go:39-48` (`codexAvailable`), callers `internal/cli/index.go:35`, `internal/cli/install_mcp.go:29` | Bootstrap branch: "no `~/.claude/projects` is OK **iff codex** dir exists" — a per-source name baked into bootstrap | "Any registered non-Claude source root is available", from registry root info; a future source-only machine bootstraps without editing bootstrap code |
| D5 | `internal/mcp/server.go:39,52,69,82,96` | Five `mcp.Enum("claude-code", "codex", "all")` + `DefaultString("claude-code")` on `search`/`ask`/`list_sessions`/`activity_summary`/`read_session` | One helper producing the enum values + default from the registry (`mcp.Enum` is variadic, so a derived `[]string` is compatible) |
| D6 | `internal/mcp/tools.go:59,96,131,176,255` | Five `req.GetString("source", "claude-code")` defaults | Default from the registry default source |
| D7 | `internal/doctor/doctor.go:38-62` | Codex dir resolved and reported as a bespoke special-case | `doctor` iterates registry sources for root presence/health |
| D8 | `internal/db/db.go:182-191` | Source-filter SQL branches on `model.SourceClaudeCode` as the implicit default | Default-source name taken from the registry (constant D1 remains the value) |
| D9 | `internal/tui/browse_view.go:249`, `internal/tui/search_view.go:229` | Literal `if ... Source == "codex" { label = "[codex] " + ... }` | `registry.Label(source)`; label for unknown/empty source unchanged (no label) |

Adapter registration itself is already centralized (`NewWithBuiltinSources` at
`internal/ingest/ingest.go:60`, `AddSource` at `:68`, `addCodexSource` at
`internal/ingest/source.go:114`) and is **not** changed here — the registry covers
names/labels/roots for the surfaces above; ingester adapter registration keeps its
existing seam.

## 2. Registry design

**Shape: a static-seeded slice + helpers.** No formal registration mechanism, no dynamic
registration API — a package-level list of `{name, label, root-resolver}` entries seeded at
compile time from the `model` constants, plus small helpers (`Names()`, `Label(name)`,
`IsValid(name)`, default-source accessor, root-availability check). Adding a source =
adding one entry to the seed.

**Static seed, pinned (not dynamic registration).** The five MCP enums are declared once
at server construction (`internal/mcp/server.go`); a compile-time-static seed guarantees
the advertised enum can never drift from what the process accepts mid-run, and makes the
golden tests meaningful. (This resolves an earlier draft's ambiguity between "static seed"
and "dynamic registration" in favor of static.)

**Derivation contract.** Each surface in §1 derives its values via the helpers; none keeps
a local literal list or a `== "codex"` branch. `all` is a filter-only pseudo-value owned by
the derivation helpers, not a registry entry.

**Golden-test gate (behavior unchanged).** With only `claude-code` + `codex` seeded,
golden tests assert string-identity with today's values:

- CLI: accepted `--source` set exactly `{"", "claude-code", "codex", "all"}`; help text
  names the same three values; default `claude-code`.
- MCP: all five tools' enum exactly `("claude-code", "codex", "all")`, default
  `"claude-code"`.
- TUI: codex rows labeled `[codex]`, claude-code rows unlabeled.
- Bootstrap: with no `~/.claude/projects` and an existing codex root, `clio index` /
  `clio install-mcp` still proceed (today's `codexAvailable` behavior).
- `doctor`: per-source lines for both sources equivalent to today's output.

## 3. Deferred design drafts (for the first real provider's change)

Recorded so the analysis is not lost; **none of this is delivered or committed to by this
change**. The structural blockers, grep-verified 2026-07-14:

### 3a. Discovery is `.jsonl`-only

`WalkSessionFiles` (`internal/ingest/walker.go:11-30`; suffix filter at `:19`) enumerates
only `*.jsonl`. It has **four non-test callers**, all of which a discovery generalization
must change together: `internal/ingest/ingest.go:84` (Claude projects dir),
`internal/ingest/ingest.go:94` (every extra source root), `internal/watcher/watcher.go:107`
(live re-walk of a changed directory), and `internal/doctor/doctor.go:328`
(`coverageBySource`). Draft: adapter-declared discovery (each source enumerates its own
candidate files under its roots); `Owns`/`sourceFor` (`internal/ingest/source.go:76`)
stays for overlap disambiguation.

### 3b. Incremental state is a byte offset

`Source.ParseFile(ing, f *os.File, startOffset int64, ...)` (`internal/ingest/source.go:24`)
plus the orchestrator's stat → `classifyChange` → fingerprint-at-offset → seek →
`newOffset = startOffset + Consumed` machinery (`internal/ingest/ingest.go:149-228`)
encode append-only byte semantics; a SQLite DB mutates in place, a Markdown file may be
rewritten wholesale. Draft: an opaque per-source cursor the shared machinery round-trips
without interpreting; JSONL sources keep `{last_byte_offset, tail_fp, head_fp}` as their
cursor. **Coupled surface:** `doctor`'s `reconcile` (`internal/doctor/doctor.go:263`)
reads `last_byte_offset` directly and compares sizes/offsets (`doctor.go:288-297`) —
cursor generalization must rework it in the same change. Acceptance for that future work
should compare **logical rows** (a deterministic dump of `sessions`/`messages`/
`ingest_state` contents), not DB file bytes — SQLite page/freelist layout makes byte
comparison unstable.

### 3c. One file = one session

`SessionIDFromPath` (`internal/ingest/source.go:24`) derives the session id from the
filename, and `startSeq` is computed per file before `ParseFile` is called
(`internal/ingest/ingest.go:199`), so the orchestration is structurally single-session-
per-file. Cursor (many conversations per DB) and Aider (many sessions per Markdown file)
need a parse that yields N sessions. Draft: parse returns a slice of per-session results;
per-`(source, uuid)` transaction/conflict handling unchanged.

### 3d. Capability flags

Draft flags `IsFallback` / `SupportsIncremental` / `ExtractsActivity` were considered and
**dropped from this change**: grep shows zero consumers in the tree today. Fallback
ownership is already expressed by registration order (`AddSource` prepends;
`claude-code` is constructed last-consulted, `internal/ingest/ingest.go:48-69`); the
other two flags have no caller until a non-incremental or no-activity source exists.
Introduce them only when the first consumer arrives.

## 4. First real provider — candidate evaluation (no ordering commitment)

The choice of the first real provider belongs to the implementation change that delivers
§3, decided there against real format documentation. Two candidates recorded side by side;
neither is committed to here.

**Honesty on evidence:** install-base numbers and current format docs were **not**
independently verified during this spec task. The assessments below are structural
reasoning, not cited documentation, and MUST be re-confirmed before implementation.

- **Aider** — one Markdown file (`.aider.chat.history.md`), many sessions delimited by
  `# aider chat started at …` headers, append-only. *For:* exercises the two hardest
  deferred generalizations (non-`.jsonl` discovery + one-file-many-sessions) on a plain,
  inspectable text format. *Against / open risk:* it combines multi-session with a byte
  cursor, and nothing in the current tree validates that combination — `startSeq` is
  per-file and single-session (§3c), so "the existing byte-cursor machinery is reusable
  for Aider" is an **unproven assumption**, not a demonstrated property. Choosing Aider
  means solving discovery + multi-session + seq/cursor interplay in one change.
- **Gemini CLI** — append-only JSON logs/checkpoints (shape unverified). *For:* closest to
  the existing adapters, so lowest implementation risk; would exercise registry +
  discovery with minimal new machinery. *Against:* exercises little of §3 — it would not
  validate multi-session or the cursor generalization, so those seams would remain
  unproven.
- Cursor (SQLite `state.vscdb`, undocumented and version-volatile schema) is recorded as a
  later stress test — the wrong first pick regardless of ordering, because its schema
  reverse-engineering risk is independent of clio's SPI design.
