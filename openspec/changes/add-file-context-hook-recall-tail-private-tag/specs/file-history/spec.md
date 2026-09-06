## ADDED Requirements

### Requirement: Per-file session history

`clio file-history <path>` SHALL print, for one absolute or cwd-relative file path, the
Claude Code sessions whose tool calls targeted that exact path, newest first. Each row
SHALL show the touch date, the session title, a session-id prefix usable with
`clio show`, and the set of tool names that acted on the file in that session (e.g.
`Read`, `Edit`, `Write`). The lookup SHALL use the indexed `tool_targets` facts of kind
`file` (exact value match after applying the ingester's own path normalisation —
absolute path, secret redaction, 512-byte cap — to the argument), SHALL join to an
existing session row (orphaned facts are ignored), SHALL sort undated facts last and print
`(undated)` for them, and SHALL NOT scan message text. It
SHALL open the index read-only, be bounded by `--limit` (default 10 sessions) and
`--since` (default unbounded), be Claude-Code-only by policy (like `recall`), and exit 0
with empty output on any error or when the file has no indexed history.

#### Scenario: File with history

- **WHEN** `clio file-history internal/search/rank.go` runs in an indexed project where
  two sessions edited that file and one only read it
- **THEN** it SHALL print three rows, newest first, each carrying date, title, id prefix,
  and the tools used (`Edit` / `Edit` / `Read`)

#### Scenario: No history is silent

- **WHEN** `clio file-history` runs for a path no indexed session ever targeted
- **THEN** it SHALL print nothing and exit 0

#### Scenario: Errors never surface

- **WHEN** the index is missing, locked for reading, or unreadable
- **THEN** it SHALL exit 0 with empty output

#### Scenario: Path normalisation

- **WHEN** the path is given relative to the working directory
- **THEN** it SHALL be resolved to the same absolute form the ingester stored (the
  `file_path` argument Claude Code passed to the tool, after the same redaction and cap),
  so relative and absolute spellings find the same rows

### Requirement: PreToolUse file-context hook

`clio file-history --hook` SHALL read a Claude Code PreToolUse hook payload from stdin
(JSON with `tool_name`, `tool_input.file_path`, `session_id`, `cwd`), SHALL take the path
from `tool_input.file_path`, and SHALL emit the digest in the hook's additional-context
form so Claude receives it alongside the Read result. The hook SHALL never block or alter the tool call:
it SHALL NOT emit a deny decision, and every failure path SHALL produce empty output with
exit 0. It SHALL skip injection when (a) the tool is not `Read`, (b) the file has no
indexed history, or (c) the file's on-disk modification time is newer than the newest
indexed touch of it, where "newest indexed touch" is taken over all session-joined facts
for the path including the calling session's (the indexed history is then stale relative
to the file); when the file cannot be stat'ed the gate SHALL be skipped. The calling
session's own touches SHALL be excluded from the printed rows — its subagents' touches
too when the caller is a parent session, and only its own rows when the caller is itself
a subagent. The digest SHALL end with a
one-line pointer to `read_session` / `clio show` for details.

#### Scenario: Read of a file with fresh history

- **WHEN** the hook receives `{"tool_name":"Read","tool_input":{"file_path":"/p/a.go"},
  "cwd":"/p"}` and `/p/a.go` has indexed touches newer than its mtime
- **THEN** stdout SHALL carry the digest in the additional-context form and the exit code
  SHALL be 0

#### Scenario: File modified after its last indexed touch

- **WHEN** the file's mtime is newer than the newest session-joined `tool_targets.ts`
  for that path (the calling session's touches included)
- **THEN** the hook SHALL print nothing and exit 0

#### Scenario: File absent on disk

- **WHEN** the path has indexed history but `stat` fails
- **THEN** the hook SHALL print the digest (gate skipped)

#### Scenario: Non-Read tool

- **WHEN** the payload's `tool_name` is anything other than `Read`
- **THEN** the hook SHALL print nothing and exit 0

#### Scenario: Malformed payload

- **WHEN** stdin is empty, not JSON, or lacks `tool_input.file_path`
- **THEN** the hook SHALL print nothing and exit 0

### Requirement: Bounded hook cost

The hook SHALL cap its output at the `--limit` row count and at 8,000 characters
(dropping rows from the tail and inserting a `(+N more)` line before the trailing
pointer line; when not even one row fits, the hook SHALL stay silent) so it stays under
Claude Code's 10,000-character hook-output limit, SHALL run at most two indexed queries per
invocation (rows, newest touch), and SHALL complete within the latency budget recorded in the design
(warm index: well under 100 ms on the reference machine; the first cold run may take
seconds and is accepted because it only happens once per boot). It SHALL NOT ingest,
SHALL NOT take the writer lock, and SHALL NOT open the index for writing.

#### Scenario: Output size cap

- **WHEN** the rows for a path would render to more than 8,000 characters
- **THEN** the digest SHALL be cut at a row boundary below that size, with a
  `(+N more)` line before the trailing pointer line

#### Scenario: Warm-index latency

- **WHEN** the hook runs against an already-cached index
- **THEN** its wall time SHALL be dominated by process start, not by the query (the
  design records the measurement method)
