## Why

clio ingests Codex CLI history (#5-A, shipped v0.9.0) but does not extract activity
targets from Codex tool calls: the target extractor and its tool→field map are hardcoded
to Claude Code tool names, so `clio activity --by command --source codex` and
`clio list --ran/--touched --source codex` surface nothing for Codex. Reverse-engineering
542 local rollout files (8,987 `function_call` records) shows command execution is 98% of
Codex tool activity — `exec_command` (`cmd` string) and `shell` (`["bash","-lc",script]`
argv) — with image views via `view_image` (`path`). A related gap: the shared
tool-use summary reads only string fields, so Codex tool-use messages currently show the
bare tool name (e.g. `exec_command`) instead of the command, in both `clio show` and FTS.

Design: `docs/superpowers/specs/2026-06-20-clio-codex-activity-targets-design.md`
(codex-reviewed 2 rounds → clean).

## What Changes

- **Added** Codex-specific activity-target extraction in the Codex adapter:
  `exec_command`/`shell` → a `command` target, `view_image` → a `file` target, every
  non-clio Codex tool → a `tool` target; clio's own MCP tools produce nothing; values are
  secret-redacted before being capped. Claude Code extraction (`extractTargets`,
  `activityField`, `BackfillActivity`) is untouched.
- **Added** a Codex tool-use summary derived from the same command/file extraction, so
  `clio show` and full-text search show the actual command (previously blank for Codex).
  The value is redacted before truncation. The shared Claude `toolUseSummary` is unchanged.

Existing Codex rows indexed by v0.9.0 gain these targets on the next `clio index --full`
(a full reindex hard-deletes `tool_targets` before re-inserting). No dedicated backfill is
added; apply_patch file-edit extraction is out of scope.

## Capabilities

### Modified Capabilities

- `session-ingest`: Codex `function_call` records produce concrete `command`/`file`/`tool`
  activity targets and a command-bearing tool-use summary; Claude Code ingestion unchanged.
