## Why

The codex adversarial review found four real defects in the incremental ingest state
machine. None corrupts the SQLite file, but each silently degrades the index:

1. **Silent message loss on an unparseable line.** `IngestFile` advances the byte
   watermark to the last complete newline, then parses; a complete line that fails to
   parse (genuine corruption, or a Claude event shape clio does not yet understand) is
   logged and skipped, but the watermark still moves past it. That message is never
   re-read until a full reindex — and the loss is invisible.
2. **Stale-snapshot re-validation ignores stat errors.** `commit()` re-stats the
   source inside the write lock to detect a concurrent rewrite, but only acts
   `if statErr == nil`. If the file was replaced/removed and the stat fails, the guard
   is skipped and a stale snapshot is committed.
3. **Unbounded read of the unread tail.** `readFrom` uses `io.ReadAll` on everything
   from the watermark to EOF. A very large tail (or a huge line with no newline yet)
   loads it all into memory, and if there is still no complete newline, the same bytes
   are re-read on every pass.
4. **Tail-only fingerprint can miss a rewrite.** Incremental resume validates only the
   512 bytes ending at the stored offset. If bytes before that window are rewritten
   while the last 512 stay identical and the file grows, the append check passes and
   stale earlier content persists.

This is the "ingest state machine" change deferred from `polish-review-p3`.

## What Changes

- **Modified** unparseable-line handling: skip the line, increment a persistent
  per-file `unparsed_lines` counter, keep advancing the watermark (so one bad line
  never wedges a file), and surface the total via `clio doctor`.
- **Modified** `commit()` re-validation: treat a stat error the same as a detected
  change — abort the pass (leave the watermark) and retry on the next pass.
- **Modified** the tail read to stream complete lines (bounded memory) instead of
  `io.ReadAll`, preserving the existing single-transaction-per-file atomicity and the
  partial-trailing-line behavior.
- **Modified** rewrite detection to validate a head fingerprint (first bytes) in
  addition to the tail fingerprint; a mismatch on either triggers full reingest.
- **Added** migration `0005` adding `ingest_state.head_fingerprint` and
  `ingest_state.unparsed_lines`.

## Capabilities

### Modified Capabilities

- `session-ingest`: "Incremental append-aware ingest" gains bad-line, bounded-memory,
  and head+tail rewrite scenarios; "Per-message size cap and atomic per-file ingest"
  gains a commit-time stat-error abort scenario.
- `diagnostics`: "Source-of-truth reconciliation" reports the count of unparseable
  lines.
