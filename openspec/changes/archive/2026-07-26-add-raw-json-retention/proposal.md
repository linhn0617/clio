# Proposal: add-raw-json-retention

## Why

clio's SQLite index is ~4.4× the size of the source files it indexes. Measured
breakdown on a real 57k-message DB: the FTS trigram index is 68% (structural —
the price of Chinese substring search, untouchable), but stored `raw_json`
(the full original event line per message, kept for `clio show --format
raw|json`) is a further ~10% (221 MB) that is **reconstructable from the source
files**. On that DB every byte of it belonged to sessions older than 14 days.
Because `raw_json` is a redaction-processed copy of a line still present in the
source `.jsonl`, pruning it for old sessions is a reversible cache eviction,
not data loss — `clio index --full` re-derives it. This gives users a way to
trade rarely-used raw-form access to old sessions for real disk space, without
touching search, content, usage, or activity data.

## What Changes

- New `clio prune-raw --older-than <dur> [--dry-run] [--vacuum]` command: blanks
  `messages.raw_json` (to the empty-string pruned sentinel) for messages of
  sessions that are old AND **demonstrably restorable** — the source file is a
  regular, readable file under a currently-scanned root for its source adapter,
  and the indexed snapshot is a completed, current ingest (size+mtime+offset
  match AND `aborted=0`; see design). Non-restorable/unverified sessions are
  skipped and counted by reason, never pruned. `--dry-run` reports what would be pruned and the reclaimable bytes
  without writing. `--vacuum` runs `VACUUM` afterward to actually shrink the
  file (otherwise the freed pages sit in the freelist until the next VACUUM).
- `clio show --format raw|json` on a pruned session degrades honestly: it emits
  the available data with a clear per-session note that the raw form was pruned
  and can be restored with `clio index --full`, rather than printing empty
  lines. `--format markdown` is unaffected (it never used `raw_json`).
- `clio doctor` reports how many messages have pruned raw_json and the
  reclaimable-on-VACUUM estimate, so the state is visible.
- A migration (0012) narrows the `messages` update trigger to
  `AFTER UPDATE OF content`, so blanking `raw_json` does NOT fire an FTS
  delete+reinsert (the prior "no migration" idea would have rewritten all FTS
  postings and churned the WAL). FTS postings, `content`, `session_usage`,
  quota, and `tool_targets` end byte-identical. Sessions with pending activity
  backfill (claude-code tool_use rows still missing `tool_targets`) are skipped
  read-only, not backfilled by this command.

## Capabilities

### New Capabilities

- `raw-json-retention`: the prune command's selection rule (age + source-file
  presence), the pruned sentinel and its restore path, the dry-run/vacuum
  behavior, and the reversibility invariant.

### Modified Capabilities

- `cli-surface`: `clio show --format raw|json` renders a pruned-session note
  instead of empty output.
- `diagnostics`: `clio doctor` reports pruned-raw counts and reclaimable bytes.

## Impact

- Code: migration `0012` (narrow `messages_au` to `AFTER UPDATE OF content`;
  add `ingest_state.aborted` successful-vs-aborted signal, existing rows
  fail-closed to 1 until a full reindex validates them);
  new `internal/cli/prune.go`; `internal/cli/show.go` (pruned-session
  rendering); `internal/sessions` (pruning query + pruned detection);
  `internal/cli/index.go` (post-full-run unrestored-prune check);
  `internal/doctor` (reporting). `raw_json` stays `TEXT NOT NULL`; the empty
  string is the pruned sentinel (a real event line is never empty).
- Data: reversible for any restorable session (`clio index --full` re-derives
  raw_json; the command only prunes when the indexed snapshot matches the
  on-disk file, and `index --full` reports+exits non-zero if any pruned session
  fails to restore, so loss is never silent).
- Non-goals (recorded so scope stays tight): automatic/background pruning
  (this command is manual and explicit — no config subsystem, consistent with
  the deliberate no-config stance of the usage change); pruning `content` or
  FTS (that would break search); on-the-fly raw reconstruction from source
  files during `clio show` (restore is via the existing `index --full`, not a
  new read path); any monetary/usage-data change.
