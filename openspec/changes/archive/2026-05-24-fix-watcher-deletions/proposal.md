## Why

The codex adversarial review found two defects around source-file lifecycle:

1. **Deleted sessions stay searchable forever.** The watcher reacts only to
   `Create|Write` events, and the 60s backstop only ingests files that still exist. When
   a `.jsonl` is deleted or moved out of the tree, its rows remain in `sessions`,
   `messages`, and the FTS index indefinitely. A user who deletes a conversation expects
   clio to forget it; instead it keeps surfacing in search — a correctness and privacy
   gap.
2. **`pidAlive` treats `EPERM` as dead.** `pidAlive` returns `proc.Signal(0) == nil`. A
   live process the caller may not signal (different owner) returns `EPERM`, so the
   lease can be wrongly judged dead and stolen.

## What Changes

- **Added** source reconciliation that purges rows for source files confirmed gone
  (`sessions`, their `messages`/FTS via the existing delete triggers, `tool_calls`, and
  `ingest_state`). Deletion is decided by an authoritative filesystem check during the
  backstop / catch-up, NOT by reacting to individual `Remove`/`Rename` events (fsnotify
  fires those during atomic temp→rename writes, so a single event is not authoritative).
  The same reconciliation helper runs in the MCP watcher backstop and in CLI catch-up,
  so deletes are reflected whether or not the MCP server is running. A file that
  reappears is simply re-ingested on the next pass.
- **Modified** `pidAlive` to treat `EPERM` as alive (process exists but is not
  signalable by us).

## Capabilities

### Modified Capabilities

- `file-watcher`: "Backstop reconciliation" purges sources confirmed gone from the
  filesystem.
