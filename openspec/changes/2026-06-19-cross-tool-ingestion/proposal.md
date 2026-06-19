## Why

clio indexes only Claude Code history. Developers increasingly run more than one
agentic CLI; the most active second tool on this machine is OpenAI Codex CLI, which
stores 530+ rich session transcripts under `~/.codex/sessions` in append-only JSONL —
the same shape clio already ingests, but with a different event schema. clio's
ingestion is hardcoded to Claude Code's format and a single source root, so none of
that history is searchable. This change makes ingestion pluggable and adds Codex as a
second source behind an opt-in `--source` filter, so default behavior is unchanged.

Design: `docs/superpowers/specs/2026-06-19-clio-cross-tool-ingestion-design.md`
(codex-reviewed 4 rounds → implementation-ready).

## What Changes

- **Added** a pluggable source-adapter SPI: each source owns its roots, file
  ownership, canonical session id, and whole-file parse/aggregation; the
  incremental/commit/FTS/redact machinery stays shared. Claude Code's existing
  ingestion moves wholesale behind the `claude-code` adapter (behavior unchanged); a
  new `codex` adapter is added.
- **Added** the Codex adapter: ingests `~/.codex/sessions/**/rollout-*.jsonl`, taking
  the canonical session id from the filename (verified against `session_meta.id`), the
  project path from the real `cwd`, and mapping the `response_item` stream
  (user/assistant/reasoning/function_call) into clio's roles — skipping the duplicate
  `event_msg` stream and stripping harness wrapper blocks.
- **Added** a `source` column on `sessions` (default `claude-code`) and fail-closed
  cross-source uuid collision handling: a uuid already owned by another source is
  detected before any write, refused loudly, and recorded in a durable
  `source_conflicts` table that `doctor` reports.
- **Modified** reads to be source-aware: `list`/`show`/`search`/`ask`/`activity`, the
  FTS engine, and the MCP tools all default to Claude-Code-only and opt in via
  `--source` / a `source` param; results carry and label their source. `recall` stays
  Claude-Code-only by policy.
- **Modified** operations for multiple roots: the watcher watches every source root,
  and deletion reconciliation is partitioned per root so a temporarily missing root
  never authorizes purging its rows; `doctor` reports per-source health, conflicts, and
  unowned files.
- **Modified** the TUI to label each row's source and accept a launch-time `--source`
  filter.

## Capabilities

### Modified Capabilities

- `session-ingest`: pluggable source adapters; Codex adapter; `source` column;
  fail-closed cross-source collision handling; source-scoped activity backfill.
- `cli-surface`: `--source` on read commands + source labels; `recall` stays CC-only.
- `fts-search`: search defaults to Claude Code, opts in to other sources.
- `mcp-server`: the read tools gain a `source` parameter (default Claude Code).
- `tui`: source label + launch `--source` filter.
- `diagnostics`: per-source health, `source_conflicts`, and unowned-file reporting.
- `file-watcher`: multi-root watching and per-root purge partitioning.
