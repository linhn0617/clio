## Why

An ambiguous session-id prefix (`clio show abc`, MCP `read_session`) fails with
"ambiguous session id prefix" and nothing else, so the user must run `clio list`
and compare ids by eye before retrying. Two small leftovers are tidied in the same
change: `AddSessionStartHook` and `RemoveSessionStartHook` were called only from
tests (install and uninstall go through `InstallClioHooks` / `RemoveClioHooks`),
and the Traditional Chinese README lacked the `usage` and `prune-raw` lines of the
English CLI list.

## What Changes

- **cli-surface**: an ambiguous prefix reports up to 10 candidates, newest first,
  each with an id long enough to tell the listed candidates apart (at least 8
  characters, or the whole id when shorter), its start time and a one-line title stripped of terminal escapes;
  a final line says when more sessions match. The error still matches
  `ErrAmbiguous`, so existing callers are unchanged.
- Delete `AddSessionStartHook` / `RemoveSessionStartHook`; their tests now exercise
  the live entry points `AddClioHooks(path, cmd, "")` and `RemoveClioHooks`.
- README_zh-TW: add the `clio usage` and `clio prune-raw` lines.

## Impact

- `internal/sessions` (ResolvePrefix query orders by start time and reads up to 11
  rows instead of 2), `internal/claudeconfig/hooks.go`, README_zh-TW.md.
- No schema, flag or MCP schema change.
