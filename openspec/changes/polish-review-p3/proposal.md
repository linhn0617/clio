## Why

The review's P3 list: small correctness/robustness/clarity fixes with no architectural
change. Three are observable (bounded large-file read, `show` ergonomics, installer backup
cleanup); the rest are internal quality (allocation, caching, error visibility, modern
idioms). Grouped into one batch since each is isolated and low-risk.

## What Changes

- **Modified** ingest to bound `readFrom` with a read cap so a huge file is ingested in
  line-aligned chunks across passes instead of read fully into memory; a single line
  exceeding the cap is skipped with a warning.
- **Modified** `show` to accept `--json` (alias for `--format json`) and `--limit` (replacing
  the hard-coded message ceiling).
- **Modified** installer config mutation to remove the `.bak` on every failure path, not
  only on success.
- Internal (no behavior change): `trimToValidUTF8`/`trimLeadingToValidUTF8` trim at the
  UTF-8 boundary instead of revalidating the whole string; a per-`Ingester`
  `excluded_tool_uses` cache replaces a per-file query, and the load/walk errors are logged
  instead of swallowed; `XDG_DATA_HOME` is honored only when absolute; `os.IsNotExist` →
  `errors.Is(err, fs.ErrNotExist)`; `internal/cli/format.go` → `display.go`; `titleFrom`
  uses `strings.Cut`.

## Capabilities

### Modified Capabilities

- `session-ingest`: bounded read of oversized source files.
- `cli-surface`: `show --json` alias and `show --limit` flag.
- `mcp-installer`: backup is cleaned up on failure paths too.
