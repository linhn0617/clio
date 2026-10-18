## Why

Every read-style CLI command (`list`, `search`, `ask`, `tui`, ...) opens the index and runs an
incremental catch-up that ends in `BackfillActivity`. Its pre-check is a correlated
`NOT EXISTS` over every `tool_use` message (35k rows on the maintainer's index): 5.4 s cold,
1.7-2.2 s warm, and the answer is almost always "nothing to backfill". Two cheaper wins ride
along: a redundant single-column index on `messages(session_uuid)` that the unique
`(session_uuid, seq)` index fully covers, and a 4 KB default read buffer on the session-file
streaming readers.

## What Changes

- **session-ingest (B3)**: the read-path catch-up runs the activity backfill only when that
  catch-up ingested something (`Stats.FilesIngested > 0`); `clio index` (including `--full`)
  keeps running it unconditionally as the safety net. MCP leader startup is unchanged.
- **session-ingest (B12)**: migration 0015 drops `idx_messages_session`; `session_uuid` lookups
  use `idx_messages_session_seq`.
- Streaming readers use a 256 KB bufio buffer (no behavior change).
- Not done (marked `// debt:`): batched/prepared inserts in `commit()`, incremental usage reads.
