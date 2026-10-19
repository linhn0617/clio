## Why

An ambiguous session-id prefix (`clio show abc`, MCP `read_session`) fails with
"ambiguous session id prefix" and nothing else, so the user must run `clio list`
and compare ids by eye before retrying. Two small leftovers are tidied in the same
change: settings-mutating hook helpers that only tests called
(`AddSessionStartHook`, `RemoveSessionStartHook`, `AddPreToolUseReadHook`,
`InstallPreToolUseReadHook`, `RemovePreToolUseReadHook`; install and uninstall go
through `InstallClioHooks` / `RemoveClioHooks`), and the Traditional Chinese
README lacked the `usage` and `prune-raw` lines of the English CLI list.

## What Changes

- **cli-surface**: an ambiguous prefix reports up to 10 candidates, top-level
  sessions before subagents (tagged `[subagent]`), each group newest first. Each
  shows the shortest id (at least 8 characters, or the whole id when shorter or
  when invalid UTF-8 is involved, see below) that resolves back to that session alone across the whole index — not merely among
  the listed ones — and is not another session's exact id; its start time; and a
  one-line title without terminal escapes. A final line says when more match. An
  id that is not plain printable text is shown quoted with escapes, so it stays
  visible and distinct.
- The error is `*sessions.AmbiguousError` and still matches `ErrAmbiguous`.
  `clio show` renders it with `clio list`'s time and title formatting; MCP returns
  its default message, which uses RFC 3339 times like every other MCP tool.
- An empty id is rejected (`ErrEmptyID`) before any query, instead of being
  treated as a prefix of every session.
- The unique-prefix path is still one query that stops after two rows; only an
  ambiguous prefix runs the ordered candidate query plus one read of the prefix's
  matches, from which every shown id is derived in memory (longest common prefix,
  with SQLite LIKE's ASCII case folding). When any in-scope matching id is invalid UTF-8
  or contains U+FFFD, SQLite's LIKE and Go's bytes disagree, so those candidates
  are listed with whole ids, which always resolve by exact match.
- The whole resolve reads one snapshot (a read-only transaction), so a
  concurrent ingest cannot make the listing disagree with the reported ambiguity.
- A blank (whitespace-only) id is rejected like an empty one. Ids containing
  non-printable runes (zero-width, bidi format characters) are shown quoted.
- `ask` reads citation metadata with an exact lookup (`sessions.GetSession`)
  rather than a prefix resolve. The RFC 3339 formatter moves to
  `timeutil.FormatRFC3339`, shared by MCP and the ambiguity message; the time
  column is padded so a missing time keeps titles aligned.
- `termsafe.OneLine` holds the one-line title rendering the CLI already used, so
  the candidate list and `clio list` share it.
- Delete the five test-only hook helpers. Their tests now drive `AddClioHooks` /
  `InstallClioHooks` / `RemoveClioHooks` and are renamed after the behavior. For the
  recall-hook tests that is `AddClioHooks(path, cmd, "")`, the `--no-file-context`
  path, which also removes clio's own file-history entry; a new test pins that it
  leaves foreign PreToolUse hooks exactly as they were. The read-only `Has*`
  predicates stay.
- README_zh-TW: add the `clio usage` and `clio prune-raw` lines.

## Impact

- `internal/sessions` (ResolvePrefix, GetSession, AmbiguousError), `internal/cli`
  (show, display), `internal/ask`, `internal/mcp`, `internal/termsafe`,
  `internal/timeutil`, `internal/claudeconfig/hooks.go`, README_zh-TW.md.
- Every resolve now runs inside one read-only transaction (a plain BEGIN, not
  the write-locking IMMEDIATE). The unique and not-found paths still run the
  exact lookup and one two-row LIKE query; an ambiguous lookup adds the ordered
  candidate query and one read of the prefix's uuids (four SELECTs in all).
- No schema, flag or MCP schema change.
