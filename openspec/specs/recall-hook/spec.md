# recall-hook Specification

## Purpose
TBD - created by archiving change 2026-06-13-ambient-recall-hook. Update Purpose after archive.
## Requirements
### Requirement: Project-scoped recall digest

`clio recall` SHALL print a concise, read-only digest of recent activity for the
current project, determined from the working directory (or a `--project`
override). The digest SHALL open with a "last session left off" section: a bounded
excerpt (default 600 runes, `--tail-runes` to tune, `0` to omit) of the final
non-blank assistant *text* message of the session shown first under "Recent
sessions", or — when that since-bounded list is empty — of the project's newest
session regardless of `--since` (same page size, so subagent nesting matches; a
promoted orphan subagent counts). The header SHALL carry that message's
timestamp (the session's end time when the message row is undated) and the
session's id prefix. Assistant turns that are tool-use only or blank SHALL be
skipped when locating it, and the section SHALL be omitted when no such message
exists. A project idle longer than the activity window therefore still opens
with where it left off, even when the other sections are empty. The digest SHALL then include the project's most recent sessions
(title, date, turn count), the files it recently touched, and the commands it
recently ran. It SHALL open the index read-only (no ingest, no write-lock
contention with a running MCP server), print nothing when the project has no
indexed history, and exit 0 with empty output on any error so it can never break
session startup.

#### Scenario: Digest for a project with history

- **WHEN** `clio recall` runs in a directory under an indexed project
- **THEN** it SHALL print that project's recent sessions, touched files, and
  commands

#### Scenario: Last session excerpt

- **WHEN** the session listed first under "Recent sessions" ends with an
  assistant text message longer than the excerpt bound
- **THEN** the digest SHALL open with that message truncated at the bound on a
  rune boundary, prefixed by the message's timestamp and the session's id prefix

#### Scenario: Project idle longer than the activity window

- **WHEN** the project's newest session ended before the `--since` cutoff
- **THEN** the digest SHALL still open with that session's excerpt while the
  recent sessions / files / commands sections are empty

#### Scenario: Excerpt matches the first listed session

- **WHEN** the since-bounded listing promotes a subagent child (its parent is
  outside the window) above an older top-level session
- **THEN** the excerpt SHALL come from that promoted child, the same session
  printed first under "Recent sessions"

#### Scenario: Last turn was tool-use only

- **WHEN** the most recent session's final assistant turn carries only a tool
  call and its preceding assistant turn carries text
- **THEN** the excerpt SHALL come from the preceding text turn

#### Scenario: No assistant text

- **WHEN** the most recent session has no assistant text message at all
- **THEN** the "last session left off" section SHALL be omitted and the rest of
  the digest SHALL print unchanged

#### Scenario: No history is silent

- **WHEN** `clio recall` runs for a project with no indexed sessions
- **THEN** it SHALL print nothing and exit 0

#### Scenario: Errors never break startup

- **WHEN** the index is missing or unreadable
- **THEN** `clio recall` SHALL exit 0 with empty output

### Requirement: Opt-in SessionStart hook installation

`clio install-hook` SHALL register a Claude Code SessionStart hook (in
`~/.claude/settings.json`) that runs `clio recall`, and — unless
`--no-file-context` is given — a PreToolUse hook with matcher `Read` and a 10-second
timeout that runs `clio file-history --hook`; `clio uninstall-hook` SHALL remove both. Installation SHALL
be atomic (leaving the original config intact on failure), SHALL preserve any
existing hooks in either event (including foreign hooks that share a group with
clio's), and SHALL be idempotent per hook (re-running adds a missing hook
without duplicating a present one). It is opt-in and SHALL
NOT be performed by `install-mcp`. clio's hook entries SHALL be recognised by
their exact command shape (`<…/clio> recall`, `<…/clio> file-history --hook`)
regardless of the binary's absolute path.

#### Scenario: Install preserves existing hooks

- **WHEN** a user runs `clio install-hook` with other SessionStart or PreToolUse
  hooks present
- **THEN** clio's two hooks SHALL be added and the existing hooks SHALL remain

#### Scenario: Re-install after an older clio installed only the recall hook

- **WHEN** the settings already hold clio's SessionStart recall hook but no
  PreToolUse file-history hook
- **THEN** `clio install-hook` SHALL add only the PreToolUse hook and leave the
  SessionStart entry untouched

#### Scenario: Opting out of file context

- **WHEN** a user runs `clio install-hook --no-file-context`
- **THEN** only the SessionStart recall hook SHALL be registered, and a file-history
  hook left by an earlier install SHALL be removed

#### Scenario: Uninstall removes only clio's hooks

- **WHEN** a user runs `clio uninstall-hook`
- **THEN** only clio's SessionStart recall hook and PreToolUse file-history hook
  SHALL be removed, leaving other hooks intact

