## MODIFIED Requirements

### Requirement: PreToolUse file-context hook

`clio file-history --hook` SHALL read a Claude Code PreToolUse hook payload from stdin
(JSON with `tool_name`, `tool_input.file_path` or `tool_input.command`, `session_id`,
`cwd`). For `Read` it SHALL take the path from `tool_input.file_path`. For `Bash` it SHALL
take candidate paths from `tool_input.command`: heredoc bodies (`<<WORD`, `<<-WORD`,
quoted delimiters) are removed first; the rest is split into words on whitespace and shell
operators (`;|&()<>`) with single/double quotes and backslashes grouping/escaping (so a
quoted path with spaces is one word), unquoted `#` comments are ignored, and an
unterminated quote yields no candidates. At most the first 64 words are examined. Words
that are empty or start with `-` or `$`, the word following a redirection operator
(`>`, `>>`, `<`), and the words of a `tee` command up to the next operator are ignored (leading/trailing `,` and `:` are trimmed from the rest); a
relative word is resolved against `cwd` (an empty or relative `cwd` yields no candidates; if
the command contains a `cd` or `pushd` word, relative words are ignored and only absolute
ones are kept, because the payload `cwd` is the directory from before the command ran and an
earlier `cd` in the same command makes the real directory unknowable) and the result cleaned. A word is a candidate only if the
path exists as a regular file and, after symbolic links in both it and `cwd` are resolved,
lies strictly under `cwd` by path elements (`/p2` is not under `/p`; a failed resolution
disqualifies it). Whether `cwd` or the command spells a path through a symlink is irrelevant. Candidates SHALL be de-duplicated and limited to the first three in
order of appearance. Each candidate SHALL be looked up and gated exactly as a `Read` path. The
hook SHALL emit the digest in the hook's additional-context
form so Claude receives it alongside the Read result. The hook SHALL never block or alter the tool call:
it SHALL NOT emit a deny decision, and every failure path SHALL produce empty output with
exit 0. It SHALL skip injection when (a) the tool is neither `Read` nor `Bash`, or a `Bash` command has no candidate (in which case it SHALL return before opening the index), (b) the file has no
indexed history, or (c) the file's on-disk modification time is newer than the newest
indexed touch of it, where "newest indexed touch" is taken over all session-joined facts
for the path including the calling session's (the indexed history is then stale relative
to the file); when the file cannot be stat'ed the gate SHALL be skipped. The calling
session's own touches SHALL be excluded from the printed rows — its subagents' touches
too when the caller is a parent session, and only its own rows when the caller is itself
a subagent. With several candidate files the digest SHALL hold one segment per file that has
fresh history (each in the single-file format), and the digest SHALL end with a single
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

#### Scenario: Other tool

- **WHEN** the payload's `tool_name` is neither `Read` nor `Bash`
- **THEN** the hook SHALL print nothing and exit 0

#### Scenario: Bash command naming a file with fresh history

- **WHEN** the payload is `{"tool_name":"Bash","tool_input":{"command":"cat a.go"},
  "cwd":"/p"}` and `/p/a.go` exists with indexed touches newer than its mtime
- **THEN** stdout SHALL carry the digest for `/p/a.go` in the additional-context form and
  the exit code SHALL be 0

#### Scenario: Bash command with no candidate file

- **WHEN** the command is `git status`, or names only missing files, directories, or files
  outside `cwd`
- **THEN** the hook SHALL print nothing, exit 0, and SHALL NOT open the index

#### Scenario: Path outside cwd

- **WHEN** the command names an existing file that lies outside `cwd` (for example under
  a sibling directory `/p2` while `cwd` is `/p`, or via `../`)
- **THEN** that word SHALL NOT be a candidate

#### Scenario: Quoted path with spaces

- **WHEN** the command is `cat "sub dir/e.go"` and that file exists under `cwd`
- **THEN** `sub dir/e.go` SHALL be treated as one candidate

#### Scenario: Heredoc body, comment and redirect target

- **WHEN** the command is `cat <<EOF` followed by a body line naming an existing file, a
  `cat a.go # b.go` comment, or `cat a.go > b.go`
- **THEN** only `a.go` (the word actually read) SHALL be a candidate; the heredoc body, the
  comment text and the redirect target SHALL NOT

#### Scenario: Symlink leaving cwd

- **WHEN** `link` under `cwd` is a symbolic link to a directory outside `cwd` and the command
  is `cat link/x.go`
- **THEN** there SHALL be no candidate

#### Scenario: Directory change earlier in the command

- **WHEN** the command is `cd internal && cat a.go`
- **THEN** the relative word SHALL be ignored (zero candidates); `cd x && cat /abs/a.go`
  with `/abs/a.go` under `cwd` SHALL still yield that one candidate

#### Scenario: Very long command

- **WHEN** an existing file is named only after the 64th word of the command
- **THEN** it SHALL NOT be a candidate

#### Scenario: More than three files

- **WHEN** the command names four existing files under `cwd`
- **THEN** only the first three in order of appearance SHALL be looked up

#### Scenario: Malformed payload

- **WHEN** stdin is empty, not JSON, or lacks `tool_input.file_path`
- **THEN** the hook SHALL print nothing and exit 0

### Requirement: Bounded hook cost

The hook SHALL cap its output at the `--limit` row count and at 8,000 characters
(dropping rows from the tail and inserting a `(+N more)` line before the trailing
pointer line; when not even one row fits, the hook SHALL stay silent) so it stays under
Claude Code's 10,000-character hook-output limit (the 8,000-character cap covers all
segments of a multi-file digest together; segments are rendered in order against the remaining
budget, and a file whose segment cannot fit what remains is dropped, leaving the rest for the
next), SHALL open the index read-only once per invocation and run at most two indexed queries per
candidate file and at most three candidate files per invocation, and SHALL complete within the latency budget recorded in the design
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
