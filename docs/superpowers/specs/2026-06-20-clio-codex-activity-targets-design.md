# clio #5-A follow-up: Codex Activity-Target Extraction — Design

- **Date:** 2026-06-20
- **Status:** Approved (brainstorming HARD GATE passed; real-data-driven; scope locked via AskUserQuestion)
- **Target release:** v0.9.1 (follow-up to #5-A, shipped v0.9.0)
- **Builds on:** #5-A cross-tool ingestion (the `source` column + Codex `codexSource` adapter, shipped v0.9.0)

## 1. Goal / problem

#5-A made clio ingest OpenAI Codex CLI history behind opt-in `--source codex`. Core
surfaces (ingest, search, ask, list, show, read_session) work. But **activity targets**
are not extracted for Codex: `clio activity --by command --source codex`,
`clio list --ran <substr> --source codex`, and `--touched` / `--by file --source codex`
surface nothing for Codex, because the target extractor is hardcoded to Claude Code tool
names.

Root cause: `extractTargets(toolName, input)` and the `activityField` map in
`internal/ingest/activity.go` key off Claude Code tool names (Edit/Write/Read→file,
Bash→command, Grep/Glob→pattern, WebFetch→url). Codex `function_call` records use
different tool names and argument shapes, so `codexSource.ParseFile` — which calls the
shared `extractTargets` — only ever produces the generic `{tool, <name>}` fact, never a
command/file fact.

This change makes Codex tool calls produce command/file activity facts, and (bundled)
fixes the empty tool-call summary for Codex so `clio show` / search show the actual command.

## 2. Real-data findings (reverse-engineered from 542 local rollout files, 8,987 function_calls)

The #5-A design (§3) assumed Codex tool names were `shell` / `apply_patch` / `update_plan`.
The real corpus corrects this:

| function_call name | count | share | argument shape | natural target |
|---|---|---|---|---|
| `exec_command` | 8053 | 89.6% | `{"cmd": "<string>", "workdir", …}` | command (`cmd`) |
| `shell` | 774 | 8.6% | `{"command": ["bash","-lc","<script>"], "workdir", …}` | command (`command[2]`) |
| `write_stdin` | 86 | 1.0% | `{"session_id", "chars", …}` | — (tool-only) |
| `update_plan` | 59 | 0.7% | `{"plan":[{status,step}…]}` | — (tool-only) |
| `view_image` | 6 | 0.07% | `{"path": "<file>"}` | file (`path`) |
| `_fetch_pr`, `_get_pr_info`, `_list_pull_request_reviews`, `_fetch_pr_comments` | 9 | 0.1% | MCP-style `{pr_number, repo_full_name}` | — (tool-only) |

Decisive facts:
- `exec_command.cmd` is **always a string** (8053/8053). `shell.command` is **always
  `["bash","-lc", <script>]`** (774/774). 100% consistent — no guessing.
- **There is no `apply_patch` tool.** File edits happen as `apply_patch` heredocs *inside*
  `exec_command`/`shell` (`bash -lc "apply_patch <<'EOF' *** Begin Patch …"`), i.e. embedded
  in the command string, not a tool target. ~44 such commands exist in the corpus.
- Commands (`exec_command` + `shell`) are **98.2%** of all Codex tool activity.

## 3. Locked decisions (via AskUserQuestion)

| # | Decision |
|---|----------|
| Scope | **Commands + images.** `exec_command` / `shell` → command, `view_image.path` → file. No apply_patch heredoc parsing (file-edit targets for Codex are out of scope). |
| Architecture | **Source-specific extractor.** A new `codexExtractTargets` in `internal/ingest/codex.go`; the shared `extractTargets` / `activityField` / `BackfillActivity` (Claude Code) are untouched. |
| Summary | **Bundled fix.** codex.go computes a Codex-aware tool-call summary from the same command/file extraction, so `clio show` / FTS show the real command. Shared `toolUseSummary` (parser.go, also used by Claude) is left untouched. |
| Backfill | Existing Codex rows refresh via `clio index --full` (documented). No dedicated Codex backfill routine — `--source codex` shipped only in v0.9.0 (opt-in), so pre-existing indexed Codex data is negligible. |
| apply_patch commands | Land as a (capped, redacted) `command` target, same as any other command — consistent with Claude storing full Bash commands. Not special-cased. |

## 4. Architecture

### 4.1 One DRY extraction helper

A single internal helper resolves the domain fact (command or file) for a Codex tool call,
feeding **both** target extraction and the summary (no second source of truth):

```go
// codexCommandTarget returns the domain activity fact (command/file) for a Codex
// tool call, or ok=false for tool-only calls. value is raw (caller redacts).
func codexCommandTarget(name string, args json.RawMessage) (kind, value string, ok bool)
```

Mapping:
- `exec_command` → (`command`, `cmd`) where `cmd` is a non-empty string.
- `shell` → (`command`, script). The `command` array is the shell argv (always
  `["bash","-lc", script]` in the corpus). Extract robustly: scan argv for the shell command
  flag — an element equal to `-c`, or a combined short flag that ends in `c` (e.g. `-lc`,
  `-ic`) — and take the **next** element as the script. This survives `["/bin/bash","-lc",X]`,
  `["bash","-l","-c",X]`, and extra flags (codex P2). Only if no such flag is found, fall back
  to `strings.Join(command, " ")`.
- `view_image` → (`file`, `path`) where `path` is a non-empty string.
- everything else (`write_stdin`, `update_plan`, `_*`, unknown) → ok=false.

### 4.2 Target extraction

```go
func codexExtractTargets(name string, args json.RawMessage) []model.ToolTarget
```
- Mirrors the shared `extractTargets` contract **fully**: `name == ""` ⇒ nil; a name with the
  `clioMCPToolPrefix` (clio's own MCP tools) ⇒ nil — so clio never indexes its own MCP traffic,
  even if clio is registered as an MCP server inside Codex too (codex P2, mirrors activity.go:33).
  Otherwise the first fact is always `{TargetTool, name}`.
- If `codexCommandTarget` returns ok, append `{kind, capValue(redactString(value))}` — redact
  **before** cap, the same order as the Claude path.
- Value is redacted (args are unredacted here — `p.Arguments` comes from the parsed envelope,
  not the redacted `raw`) and capped at `maxTargetValueBytes` (512B), identical to the Claude path.

### 4.3 Summary

codex.go's `function_call` branch currently does `summary := toolUseSummary(args)`, which
returns "" for the `cmd` / array shapes, leaving the message content as the bare tool name.
Replace with a Codex-aware summary derived from the same helper:

```go
kind, value, ok := codexCommandTarget(p.Name, args)
summary := ""
if ok {
    summary = firstLine(redactString(value), 200) // redact BEFORE truncating
}
```
- **Redaction order matters (codex P1).** Redact the *full* value first, then take the first
  line / cap. Truncating first (as the shared `toolUseSummary` does) can slice through a secret
  so the redaction regex no longer matches, leaking a partial token into `messages.content` /
  `tool_calls.params_summary` / FTS. The new Codex path redacts first. The pre-existing
  truncate-then-redact order in `toolUseSummary` (parser.go:168, Claude path) is a *separate*
  pre-existing issue, **out of scope here** and flagged to the maintainer (§7).
- Message `Content` becomes `strings.TrimSpace(p.Name + " " + summary)` (e.g.
  `exec_command git status --short`); `ToolCall.ParamsSummary` carries the same — so `clio show`
  and the FTS content for that message now show the actual command/file.
- `toolUseSummary` in parser.go is not modified (Claude path unchanged).

### 4.4 Call-site change

In `codexSource.ParseFile`, the `function_call` case:
- `extractTargets(p.Name, args)` → `codexExtractTargets(p.Name, args)`
- `toolUseSummary(args)` → the Codex-aware summary above.

No other files change. Claude Code ingestion is byte-for-byte unaffected.

## 5. Query layer — unchanged (verified by test)

#5-A already made the read path source-aware: `ActivityByKind` / `ActivitySummary`
(internal/sessions) and `list --ran/--touched/--by` honor `--source`, reading from
`tool_targets`. The only missing piece was Codex rows in `tool_targets`. So no query-layer
change is expected; the TDD end-to-end test (§6.3) proves it — a failing test for
`--by command --source codex` goes green purely from the new extraction.

`clio recall` stays Claude-Code-only (unchanged).

## 6. Testing (TDD, red→green)

All DB tests run against a **temporary** database (temp dir or absolute `XDG_DATA_HOME`
redirect). The live DB (`~/Library/Application Support/clio/db.sqlite`) is never touched.

### 6.1 Fixture
Add a small **redacted** Codex rollout fixture under `internal/ingest/testdata/` containing
one of each: `exec_command`, `shell` (`bash -lc`), `view_image`, `update_plan`, `write_stdin`,
and one `exec_command` whose `cmd` is an `apply_patch` heredoc. Reuse / extend any existing
Codex test fixture from #5-A.

### 6.2 Unit — `codexExtractTargets` / `codexCommandTarget`
Table-driven: each tool name + representative args → expected `[]ToolTarget` (tool fact
first; command/file fact where applicable; tool-only for the rest). Cases:
- `exec_command` → command; `shell` `["bash","-lc",X]` → command (`X`).
- `shell` argv variants `["/bin/bash","-lc",X]` and `["bash","-l","-c",X]` → command (`X`), **not**
  the wrapper argv (codex P2).
- `shell` with no `-c`/`-lc` flag → `strings.Join` fallback.
- `view_image` → file (`path`); `write_stdin` / `update_plan` / `_fetch_pr` / unknown → tool-only.
- A `clioMCPToolPrefix` name (`mcp__clio__*`) → nil (codex P2).
- apply_patch-as-command (`cmd` is an `apply_patch` heredoc) → command (capped).
- **Redaction (codex P1): a command/path carrying a secret pattern → both the target value and
  the derived summary are fully redacted, with no partial leak even when the secret sits past
  byte 200.**

### 6.3 Ingest + query end-to-end
Parse the fixture through `codexSource.ParseFile`, commit into a temp DB, then assert:
- `ActivityByKind(ctx, db, "command", source="codex", …)` returns the exec/shell commands.
- `ActivityByKind(ctx, db, "file", source="codex", …)` returns the view_image path.
- `list --ran "<substr>" --source codex` matches the command; `--touched` matches the image path.
- The function_call message's content/summary contains the actual command (summary fix).
- Default (no `--source`, i.e. claude-code) does **not** surface these Codex facts.

## 7. Out of scope
- apply_patch heredoc → file-path extraction (Codex `--touched` / `--by file` for edits). Deferred.
- Any change to Claude Code extraction, `activityField`, or `BackfillActivity`.
- A dedicated Codex backfill routine (use `clio index --full`).
- `update_plan` step text, `write_stdin` content, or PR-MCP args as targets.
- **Flagged, not fixed:** `toolUseSummary` (parser.go:168) truncates before redacting — a
  pre-existing partial-secret-leak risk on the **Claude** path. The new Codex summary avoids it
  (§4.3); fixing the shared helper is a separate change, deferred.

## 8. Downstream workflow
openspec change (`validate --strict`) → TDD (this spec) → codex review of the real diff
(multi-round to clean) → Claude `/review` → PR (await authorization) → merge → openspec
archive → release v0.9.1 (CHANGELOG + README×2 version bump + tag → release.yml).

## 9. Codex design-review log
- **Round 1 (2026-06-20):** 1 P1 + 2 P2 — summary redaction order (P1), missing
  `clioMCPToolPrefix` exclusion (P2), weak `shell` argv parsing (P2). All three addressed in §4.
  Codex independently confirmed the read path is already source-aware (query layer needs no
  change) and that `index --full` hard-deletes `tool_targets` before re-insert (no stale/double
  rows).
