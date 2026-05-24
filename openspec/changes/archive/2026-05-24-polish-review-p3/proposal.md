## Why

The review's P3 list: small correctness/robustness/clarity fixes with no architectural
change. Two are observable (`show` ergonomics, installer backup cleanup); the rest are
internal quality (allocation, error visibility, modern idioms). Grouped into one batch since
each is isolated and low-risk.

(The review's "bounded large-file read" idea was dropped from this batch: a correct
implementation must change the incremental ingest state machine so a partially-read file
resumes on the next pass — that is a feature, not polish, and is deferred to its own change.
The `fallbackProjectPath` `_`-collision comment the review asked for already exists.)

## What Changes

- **Modified** `show` to accept `--json` (alias for `--format json`) and `--limit` (replacing
  the hard-coded message ceiling; non-positive falls back to the default ceiling).
- **Modified** installer config mutation to remove the `.bak` on every failure path, not
  only on success.
- Internal (no behavior change): `trimToValidUTF8`/`trimLeadingToValidUTF8` trim at the
  UTF-8 boundary instead of revalidating the whole string; the `excluded_tool_uses` load
  error is logged instead of swallowed; `WalkSessionFiles` walk errors are logged instead of
  swallowed; `XDG_DATA_HOME` is honored only when absolute; `os.IsNotExist` →
  `errors.Is(err, fs.ErrNotExist)`; `internal/cli/format.go` → `display.go`; `titleFrom`
  uses `strings.Cut`.

## Capabilities

### Modified Capabilities

- `cli-surface`: `show --json` alias and `show --limit` flag.
- `mcp-installer`: backup is cleaned up on failure paths too.
