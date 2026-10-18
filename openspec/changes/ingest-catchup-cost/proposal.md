## Why

Every read-style CLI command (`list`, `search`, `ask`, `tui`, ...) opens the index and runs an
incremental catch-up that ends in `BackfillActivity`. Its pre-check is a correlated
`NOT EXISTS` over every `tool_use` message (35k rows on the maintainer's index): 5.4 s cold,
1.7-2.2 s warm, and the answer is almost always "nothing to backfill". Two cheaper wins ride
along: a redundant single-column index on `messages(session_uuid)` that the unique
`(session_uuid, seq)` index fully covers, and a 4 KB default read buffer on the session-file
streaming readers.

## What Changes

- **session-ingest (B3)**: migration 0016 adds an initially empty activity backfill
  watermark. Every read-path catch-up and `clio index` checks only message IDs above
  the watermark using a primary-key range. Examined IDs advance even when no targets
  can be extracted; failures roll back targets and progress together. Unchanged reads
  do no write. Upgrades backfill historical rows even with unchanged transcripts.
- **session-ingest (B12)**: migration 0015 drops `idx_messages_session`; `session_uuid` lookups
  use `idx_messages_session_seq`.
- Streaming readers use a 256 KB bufio buffer (no behavior change).
- Not done (marked `// debt:`): batched/prepared inserts in `commit()`, incremental usage reads.
