## Why

A survey of claude-mem (thedotmack/claude-mem @ ad3dcaa, 2026-09-06; report in the
session scratchpad, verifier-checked) found three of its designs that clio can adopt
**without** taking on what makes claude-mem heavy: no LLM calls, no daemon, no vector
store, no network. Each one is a small, read-only-against-`~/.claude` feature that uses
data clio already indexes:

1. **Per-file history injected before Claude reads a file.** claude-mem's PreToolUse(Read)
   hook (`src/cli/handlers/file-context.ts`) shows Claude a timeline of what past sessions
   did to the file it is about to open. clio already stores every Read/Edit/Write/
   MultiEdit/NotebookEdit target in `tool_targets` (`internal/ingest/activity.go:16-26`,
   kind `file`, indexed on `(kind, value)` by `0006_tool_targets.sql`), but no command
   gives a per-file timeline: `activity --by file` returns counts only
   (`internal/cli/activity.go`), and `list --touched` / `search --touched` filter sessions or
   hits by path *prefix* without saying when each session touched the file or with which
   tools.
2. **"Where the last session left off."** claude-mem's Stop hook summarises the session
   from the *last assistant message* (`src/sdk/prompts.ts:192-224`). Claude's own closing
   message already says what was done and what is next, so clio can surface it with zero
   generation. Today `GetRecall` (`internal/sessions/sessions.go:361-376`) returns only
   session titles and file/command counts; `formatRecall` (`internal/cli/recall.go:137-163`)
   renders exactly those three sections.
3. **User-controlled privacy exclusion.** claude-mem strips `<private>…</private>` blocks,
   content included, before storage (`src/utils/tag-stripping.ts:4-17`). clio's ingest
   redacts secret *shapes* (`internal/ingest/redact.go`) but has no way for a user to say
   "do not index this paragraph". A user who pastes sensitive prose into a prompt today has
   no opt-out short of deleting the session file.

Why now: the survey is fresh, the data model needs no migration for any of the three, and
all three fit the existing `install-hook` / `recall` / redaction seams rather than opening
new ones.

## What Changes

- **Added** `clio file-history <path>`: prints, for one file path, the sessions that
  touched it (date, session title, session id prefix, which tools acted on it), newest
  first, bounded by `--limit` and `--since`. Read-only, `db.OpenReadOnly`, exit 0 with empty
  output on any error — same contract as `recall`. Claude-Code-only by policy, like recall.
- **Added** an opt-in **PreToolUse hook** (`Read` matcher) that runs
  `clio file-history --hook`, reading the hook payload from stdin and returning the digest
  as additional context to Claude.
  Guard rails copied from claude-mem's handler: skip when the file has no indexed history;
  skip when the file on disk is newer than the newest indexed touch (stale history is worse
  than none); cap rows and output size; never block the tool call.
- **Modified** `clio install-hook` / `uninstall-hook` to register/remove the PreToolUse hook
  alongside the existing SessionStart recall hook (one command still installs "clio's
  hooks"; a `--no-file-context` flag opts out of the new one). Idempotent; preserves
  foreign hooks; atomic write — the existing `claudeconfig.mutate` path.
- **Modified** `clio recall` digest: a new leading section "Last session left off" with a
  bounded excerpt (default 600 runes, flag-tunable) of the last *text* assistant message
  of the project's most recent session as `clio list` orders it (tool-use-only turns are
  skipped). Omitted when there is no
  such message. Existing sections unchanged.
- **Added** `<private>…</private>` stripping at ingest: the whole element including its
  content is removed from both the FTS `content` and the stored `raw_json` of user and
  assistant text, and from the session title. A bounded regex over text fields, applied
  next to the existing redaction, never touching the source JSONL. Documented in README as
  "clio does not index it; Claude Code itself still saw it".
- **No** schema migration, **no** new dependencies, **no** network calls.

## Capabilities

### New Capabilities
- `file-history`: the `clio file-history` command and the opt-in PreToolUse(Read) hook
  that injects a file's past-session timeline before Claude reads it.

### Modified Capabilities
- `recall-hook`: (a) the digest gains a "last session left off" excerpt requirement;
  (b) `install-hook`/`uninstall-hook` now manage the PreToolUse file-context hook as well
  as the SessionStart hook.
- `session-ingest`: new requirement — `<private>` blocks are excluded from indexed
  `content`, `raw_json`, and titles.
- `cli-surface`: `file-history` joins the subcommand list; `recall` gains the
  `--tail-runes` flag; `install-hook` gains `--no-file-context`.

## Impact

- **Code**: `internal/cli/` (new `file_history.go`; `recall.go`, `install_hook.go`,
  `root.go` edits), `internal/sessions/` (new file-history query, last-assistant-message
  query; `Recall` struct gains a field), `internal/claudeconfig/hooks.go` (PreToolUse
  add/remove/has, generalised from the SessionStart-only helpers), `internal/ingest/`
  (`redact.go`: tag stripping at the `redactString` entry; `activity.go`: a shared
  `NormalizeTargetPath` = `capValue(redactString(…))` helper).
- **Data**: none. Reads `tool_targets`, `tool_calls`, `messages`, `sessions` as they are.
  Sessions ingested before this change keep any `<private>` text until the next
  `clio index --full` (documented; the source JSONL is never modified).
- **User-facing**: one new command, two new flags, one new hook registration behind the
  existing opt-in `install-hook`. Users who already ran `install-hook` get the PreToolUse
  hook only if they run it again (the command is idempotent for the SessionStart part).
- **Docs**: README (EN/zh-TW) and `docs/USAGE.md` — command table, hook section, privacy
  section.
- **Risk**: the PreToolUse hook adds a subprocess to every `Read`. Mitigation: read-only
  DB open, indexed exact-match query, hard row caps, and the hook's own timeout; the design
  records a latency budget and how it is measured.
- **Not in scope**: LLM-generated summaries, a write-side "remember" MCP tool, vector
  search, stripping `<system-reminder>` (measured on the live DB: 51 of 3,578 user
  messages, ~7 KB of 9.7 MB — not worth a rule).
