## ADDED Requirements

### Requirement: `clio index --purge-missing` deletes retained sessions

`clio index --purge-missing` SHALL run the normal index pass, then the reconciliation marking
pass, then delete the rows of every tracked source file that is still missing, and print
`purged: N session source(s) whose transcript no longer exists`. Without the flag no command
SHALL delete a session because its source file is gone. Like `--full`, the flag SHALL be
refused with a non-zero exit and a hint to re-run `clio index --purge-missing` after stopping
Claude Code while `clio mcp` holds the index lock. When the largest group of still-missing
sources marked within 24 hours (inclusive) of the group's earliest mark is both
more than 10 files and more than half of the tracked corpus, it SHALL delete nothing, exit
non-zero, and say how many files went missing together and that `--force` deletes anyway.
Sources marked more than 24 hours apart (e.g. Claude Code's batched cleanup) SHALL NOT add
up to a refusal. `--force` SHALL skip this check and SHALL be an error without `--purge-missing`.

#### Scenario: Retained session is deleted on request

- **WHEN** a session is retained and the user runs `clio index --purge-missing`
- **THEN** its rows SHALL be deleted and the purged count printed

#### Scenario: Plain index keeps retained sessions

- **WHEN** the user runs `clio index` without the flag
- **THEN** retained sessions SHALL remain

#### Scenario: Refused while the MCP writer holds the lock

- **WHEN** `clio mcp` holds the index lock and the user runs `clio index --purge-missing`
- **THEN** the command SHALL exit non-zero, name `clio index --purge-missing` in its hint,
  and delete nothing

#### Scenario: Mass disappearance is refused

- **WHEN** most tracked files of a large corpus are missing at once
- **THEN** `clio index --purge-missing` SHALL delete nothing and exit non-zero, naming
  `--force` in its message, and SHALL keep refusing while those files stay missing

#### Scenario: Batched deletions accumulate without a refusal

- **WHEN** small batches of transcripts go missing more than 24 hours apart until they
  add up to most of the corpus
- **THEN** `clio index --purge-missing` SHALL delete them all

#### Scenario: Force overrides the cap

- **WHEN** the user runs `clio index --purge-missing --force` after a mass disappearance
- **THEN** every still-missing source SHALL be deleted; `clio index --force` alone SHALL
  exit non-zero

## MODIFIED Requirements

### Requirement: Subagent sessions are nested under their parent in the CLI

`clio list` SHALL list only top-level sessions by default, excluding subagent child
sessions (those with a populated `parent_session` whose parent session exists), and
SHALL annotate a parent that spawned subagents with a `(+N subagents)` marker. A child
SHALL be hidden whenever its parent is indexed and passes the same filters as the listing,
independent of the page size (`--limit`): a parent ranking below the limit still hides its
children. A
`--include-subagents` flag SHALL include subagent sessions in the listing. A
subagent whose parent session is not indexed, or is excluded by the listing's own filters
(for example older than `--since`, or in another project), SHALL still be listed (orphan
promotion), so nothing becomes invisible.

#### Scenario: Subagents are hidden from the default list

- **WHEN** the user runs `clio list` and a parent session spawned 3 subagents
- **THEN** the 3 subagent sessions SHALL NOT appear as separate rows, and the parent
  SHALL be annotated `(+3 subagents)`

#### Scenario: Subagents can be included

- **WHEN** the user runs `clio list --include-subagents`
- **THEN** subagent sessions SHALL appear in the listing

#### Scenario: An orphan subagent stays visible

- **WHEN** a subagent session's parent is not indexed
- **THEN** that subagent SHALL still be listed at top level

#### Scenario: Parent beyond the page limit still hides its child

- **WHEN** `clio list --limit 1` runs, the most recent session is a subagent whose parent
  is indexed but ranks below the limit
- **THEN** the subagent SHALL NOT appear and the page SHALL show the next top-level session

#### Scenario: A parent filtered out by the listing's filters promotes the child

- **WHEN** `clio list --since 1d` runs and a recent subagent's parent is older than the cutoff
- **THEN** that subagent SHALL be listed at top level

### Requirement: Activity filters and summary in the CLI

`clio list` and `clio search` SHALL accept `--touched <path-prefix>`, `--tool
<name>`, and `--ran <command-substring>` to filter results by indexed activity. A
new `clio activity --by file|command|tool|pattern|url [--since <when>] [--project
<path>]` SHALL report counts grouped by the chosen activity kind, most frequent
first. The `--project <path>` filter of `clio list` and `clio activity` SHALL match
sessions whose project path equals `<path>` or lies beneath it (a `/` boundary, a
trailing `/` ignored), never a sibling that merely shares the prefix.

#### Scenario: Filter sessions by touched file

- **WHEN** the user runs `clio list --touched /x/auth.ts`
- **THEN** only sessions whose tool calls touched a path under `/x/auth.ts` SHALL
  be listed

#### Scenario: Filter search by tool

- **WHEN** the user runs `clio search "race" --tool Bash`
- **THEN** only matches from sessions that ran a `Bash` tool_use SHALL be returned

#### Scenario: Activity grouped by command

- **WHEN** the user runs `clio activity --by command --since 7d`
- **THEN** the system SHALL list commands run in the last 7 days with their counts

#### Scenario: Project filter respects the path boundary

- **WHEN** the user runs `clio list --project /x/foo` with sessions under `/x/foo`,
  `/x/foo/sub`, and `/x/foo-bar`
- **THEN** the first two SHALL be listed and `/x/foo-bar` SHALL NOT

## ADDED Requirements

### Requirement: Terminal escape sequences are stripped from human-readable output

Human-readable CLI output SHALL NOT replay terminal control sequences carried in
indexed text. Transcripts can contain, for example, OSC 52 clipboard writes,
window-title changes, or screen clears. The human-readable output of `clio show`
(markdown), `list`, `search`, `ask`, `activity`, `recall`, `usage`, and
`file-history` SHALL strip such sequences from every indexed string it prints
(titles, project paths, snippets, excerpts, message content, tool names, activity
values): ESC-introduced CSI, OSC, DCS, APC, PM, SOS, and other ESC sequences (an
unterminated one is dropped to the end of the string, unless a new ESC appears
first — that aborts the earlier sequence and parsing restarts there, as a terminal
does), C0 control characters other than newline and tab (carriage return included),
DEL, and C1 control characters, whether UTF-8 encoded or a raw byte outside a valid
UTF-8 sequence; a C1 CSI, OSC, DCS, SOS, PM, or APC introducer or ST acts like its
ESC form.
Ordinary text, CJK, and emoji SHALL be preserved. Machine formats are unaffected:
`--format json`, `--format raw`, `--json`, the file-history hook's JSON envelope,
and MCP responses SHALL keep the stored text, with control characters escaped by
the JSON encoder.

#### Scenario: Escape sequences in a session are not replayed by show

- **WHEN** a session message contains `ESC ] 52 ; c ; <base64> BEL` and `ESC [ 2 J`
  and the user runs `clio show <id>`
- **THEN** the output SHALL contain neither `ESC` nor `BEL`, and the surrounding
  visible text SHALL still be printed

#### Scenario: Escape sequences in a recall digest

- **WHEN** the last assistant excerpt, a session title, a touched file, or a command
  contains terminal escape sequences and `clio recall` renders the digest
- **THEN** the digest SHALL contain no `ESC` byte

#### Scenario: JSON and raw output are unchanged

- **WHEN** the user runs `clio show <id> --format json` or `--format raw` on the
  same session
- **THEN** the stored message text SHALL be emitted as stored (control characters
  JSON-escaped in `json`), not stripped
