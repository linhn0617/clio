## Why

Every read-style CLI command (`list`, `search`, `ask`, `tui`, ...) opens the index and runs an
incremental catch-up that always calls `BackfillActivity`, including unchanged-file upgrades.
The original pre-check was a correlated `NOT EXISTS` over every `tool_use` message (35k rows on the maintainer's index): 5.4 s cold,
1.7-2.2 s warm, and the answer is almost always "nothing to backfill". Two cheaper wins ride
along: a redundant single-column index on `messages(session_uuid)` that the unique
`(session_uuid, seq)` index fully covers, and a 4 KB default read buffer on the session-file
streaming readers.

## What Changes

- **session-ingest (B3)**: migration 0016 adds an initially empty activity backfill
  watermark. Every read-path catch-up checks only message IDs above
  the watermark using a primary-key range. Both incremental `clio index` and
  `clio index --full` retain the full-history, watermark-ignoring repair pre-check.
  Examined IDs advance even when no targets can be extracted; failures roll back targets and progress together.
  The backfill performs no write on a caught-up read; existing PurgeMissing and PRAGMA optimize behavior may still write. With no pending
  targets but newer IDs, watermark advancement is best-effort and non-blocking:
  a busy writer leaves progress unchanged without a backfill warning. Upgrades backfill historical rows
  even with unchanged transcripts.
- **prune-raw**: preflight and transactional guards share the read-backfill definition
  of pending: an unexamined Claude Code tool message above the watermark without
  targets. Examined unfillable rows no longer block pruning. Unexamined rows keep
  their raw_json until examined; explicit repair of pruned history requires `--full`.
- **session-ingest (B12)**: migration 0015 drops `idx_messages_session`; `session_uuid` lookups
  use `idx_messages_session_seq`.
- Claude and Codex message readers use a 256 KB bufio buffer; the full-file usage
  scan retains its separate 512 KB buffer (no behavior change).
- Not done (marked `// debt:`): batched/prepared inserts in `commit()`, incremental usage reads.
