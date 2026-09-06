## Context

Three independent features share one change because they borrow from the same survey
(claude-mem, 2026-09-06) and touch the same three seams: the opt-in hook installer
(`internal/claudeconfig/hooks.go`, `internal/cli/install_hook.go`), the recall digest
(`internal/sessions/sessions.go` `GetRecall`, `internal/cli/recall.go`), and ingest-time
redaction (`internal/ingest/redact.go`, called from `internal/ingest/parser.go:75,88,102,117`
and `internal/ingest/activity.go:49`).

Facts the design leans on (grep-verified 2026-09-06):

- `tool_targets(message_id, session_uuid, ts, kind, value)` with index
  `idx_tool_targets_kind_value(kind, value)` (`0006_tool_targets.sql`). Kind `file` rows are
  written for Read / Edit / Write / MultiEdit / NotebookEdit with the tool's `file_path`
  (`internal/ingest/activity.go:16-26`); the tool name for the same `message_id` is in
  `tool_calls.tool_name` (`0001_init.sql:51-55`) and as a kind `tool` row.
- `messages.role` distinguishes assistant *text* (`assistant`) from `tool_use`, `thinking`
  and `tool_result` rows (`internal/ingest/parser.go:112-134`), so "last assistant text
  message" is `role='assistant' ORDER BY seq DESC LIMIT 1` — no content sniffing needed.
- Every persisted text field reaches `redactString` (`redact.go`) except one
  practically unreachable fallback: FTS content, titles, tool summaries and target values
  call it directly; the `raw_json` line goes `redactJSON` → `redactWalk` → string leaves →
  `redactString` (`redactWalkDepth`'s `string` case). The exception is `redactJSON`'s decode-failure path
  (plain `Redact` on the raw line), which the parser's own
  `json.Unmarshal` at `parser.go:62` already guards against; D8 notes it. `redactString` is
  therefore the entry point for `<private>`.
- `clio recall` already reads a hook payload from piped stdin (`readStdinIfPiped`,
  `parseHookCwd`) and swallows every error (`recallDigest` in `recall.go`).
- Official hooks reference (code.claude.com/docs/en/hooks, fetched 2026-09-06): PreToolUse
  JSON output supports `hookSpecificOutput.additionalContext` — "String added to Claude's
  context alongside the tool result", wrapped in a system reminder that Claude reads on
  the next model request; `permissionDecision` is optional (exit 0 with no decision =
  normal permission flow); matcher `Read` is an exact tool-name match; for Read/Edit/Write
  `tool_input.file_path` "is always absolute"; command hooks default to a 600 s timeout and
  have an `async` flag (default false); hook output strings including `additionalContext`
  are capped at 10,000 characters (longer output is written to a file and replaced by a
  path + preview).
- Latency baseline on the reference machine (`/usr/bin/time clio recall --project …`,
  3 runs): first run 2.69 s (cold page cache), then 0.05 s, 0.05 s.

## Goals / Non-Goals

**Goals:**
- Give Claude "who touched this file, when, with which tools" before it reads a file,
  with the same never-block, never-fail-loud contract as `recall`.
- Open every new session with Claude's own last words from the previous session.
- Let a user exclude a span of text from clio's index with one tag.
- Zero schema changes, zero new dependencies, zero network.

**Non-Goals:**
- Any summary *generation* (LLM or heuristic) — the tail excerpt is verbatim.
- A write-side MCP tool (`remember`) — separate decision, separate change.
- Stripping harness-injected `<system-reminder>` blocks — measured at 51 / 3,578 user
  messages (~7 KB of 9.7 MB) on the live index; not worth a rule.
- Redacting `<private>` from sessions already indexed — `clio index --full` re-ingests;
  the source JSONL is never touched.
- Watching for Edit/Write in the PreToolUse hook — only `Read` (the moment Claude wants
  context; on Edit it already has the file open).

## Decisions

### D1. One command serves both the CLI and the hook: `clio file-history <path> [--hook]`

`--hook` switches input to the PreToolUse stdin payload and output to the JSON
additional-context form; without it the command prints plain text for humans. The
installer registers `<exe> file-history --hook`.

*Alternatives*: (a) sniff stdin for `hook_event_name` as `recall` sniffs `cwd` — rejected:
the output *shape* must change (JSON vs text), and an explicit flag is easier to reason
about, test, and match in `uninstall-hook`; (b) a separate `clio hook pre-read` command —
rejected: two code paths for one query, and users lose a human-usable CLI form.

Hook-mode output when there is something to say:

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"<digest>"}}
```

No `permissionDecision` is emitted (the reference marks it optional; omitting it leaves
the normal permission flow untouched). Nothing to say → empty stdout, exit 0.

### D2. The query is one indexed join, rolled up to the top-level session

```sql
SELECT COALESCE(p.uuid, s.uuid), COALESCE(p.title, s.title),
       MAX(tt.ts), GROUP_CONCAT(DISTINCT tc.tool_name)
FROM tool_targets tt
JOIN sessions s  ON s.uuid = tt.session_uuid
LEFT JOIN sessions p ON p.uuid = s.parent_session
JOIN tool_calls tc ON tc.message_id = tt.message_id
WHERE tt.kind = 'file' AND tt.value = ?            -- idx_tool_targets_kind_value
  AND <source filter: claude-code>
  AND s.uuid <> ? AND COALESCE(p.uuid, s.uuid) <> ? -- the calling session, hook mode
GROUP BY 1 ORDER BY 3 DESC NULLS LAST LIMIT ?
```

`tool_targets.ts` is nullable (`nullZero` at `ingest.go:525`); `MAX` ignores NULLs and
`NULLS LAST` keeps undated rows at the end, printed with `(undated)` in place of the date.

**`newestTS`** (the staleness reference, D4) is a second scalar query over the *same*
WHERE clause **minus** the calling-session exclusion and **without** LIMIT:
`SELECT MAX(tt.ts) … JOIN sessions s … WHERE tt.kind='file' AND tt.value=? AND <source>`.
The `JOIN sessions` matters: the live index holds orphan `tool_targets` rows whose session
was deleted (no FK), and an unjoined MAX would let them push `newestTS` forward. Including
the calling session means a file this session just edited is gated out of injection on
its next Read (its mtime is newer than the touch event by design) — accepted, since that
edit is already in Claude's context.

Subagent touches roll up to their parent (same COALESCE pattern as `ActivitySummary`),
so one row per human-visible session; the title is `MAX(COALESCE(p.title, s.title, ''))`
so the grouped value is deterministic. The calling session (`session_id` in the hook
payload) is excluded from the rows: its own earlier reads are already in Claude's
context window. Both predicates are needed — `s.uuid <> ?` drops a subagent caller's own
rows, `COALESCE(p.uuid, s.uuid) <> ?` drops a parent caller's subagents' rows (codex and
verifier both hit the subagent-caller case).

*Alternative*: reuse `ListSessions{TargetKind:"file", TargetValue:path}` (`sessions.go:139`)
— rejected: it cannot return the per-session tool set or the touch time without a second
query per row.

### D3. Exact-value match after the ingester's own normalisation; no symlink resolution

Stored `value` is `capValue(redactString(file_path))` (`activity.go:49`): the path Claude
passed, secret-redacted and capped at 512 bytes (`activity.go:12`). The lookup key is built
the same way — `filepath.Abs` for CLI input (hook input is already absolute per the
official reference), then the same redact + cap — so a path that was altered at ingest
still matches. That helper is `ingest.NormalizeTargetPath`; the CLI applies it before calling
`sessions.FileHistory` (see Open Question 0 for why `sessions` must not import `ingest`). `EvalSymlinks` is deliberately not applied —
it would change the key for every historic row and costs a syscall per invocation. Stored
values are absolute because Claude Code passes absolute `file_path` to Read/Edit/Write
(official hooks reference, verified 2026-09-06); a relative stored value (only possible
from another source, which the lookup filters out anyway) would not match.
Prefix/LIKE matching (as `--touched` does) is not used: a per-file timeline wants the
file, not the directory.

### D4. Staleness gate: skip when the file on disk is newer than its newest indexed touch

`os.Stat(path).ModTime().Unix() > newestTS` (as defined in D2: same path/source filter,
session-joined, no LIMIT, no caller exclusion). Newer → print nothing. If the stat fails
(file does not exist yet) the gate is skipped and history prints. Copied from claude-mem's handler (`file-context.ts:255-262`), where the
rationale is that a timeline of a file that has since changed can mislead more than help.
Side effect accepted: an edit made outside Claude, or inside a session the watcher has not
ingested yet (≤ 60 s backstop), hides the history until the next touch is indexed.
*Alternative*: show the rows with a "file changed since" banner — recorded as an open
question; the spec pins skip for now so behaviour is testable either way.

### D5. Not `async`, explicit short timeout

The reference says a synchronous PreToolUse hook's `additionalContext` is added
"alongside the tool result" for the next model request; an `async` hook's output lands
whenever it finishes, detached from the Read. We want the timeline attached to *this*
Read, so the hook runs synchronously (claude-mem marks its equivalent `async: true`
because its worker already holds the data in RAM and it tolerates late arrival). The installer writes
`"timeout": 10` (seconds) instead of inheriting the 10-minute default; the warm path is
10–20 ms (measured below) and the cold path (first run after boot) ~3 s by analogy with
`clio recall`, both comfortably inside. On timeout Claude Code drops the hook's output and proceeds — same
outcome as our own empty-output path. The digest is additionally capped at 8,000
characters (rows dropped from the tail, then a `(+N more)` line) to stay under the
reference's 10,000-character hook-output limit, past which Claude only gets a file path.

Measurement method, to be repeated in the tasks: `for i in 1 2 3; do /usr/bin/time -p
clio file-history --hook <<< '<payload>' ; done`, record `real`.

**Measured 2026-09-06 (task 5.5)** on the reference machine against the live 2.6 GB
index, payload for `internal/search/rank.go`: warm `real 0.02 / 0.01 / 0.01` (this
session) and `0.01 / 0.01 / 0.01` (independent verifier build). Cold start was not
isolated for this command; `clio recall`, which opens the same DB the same way, measured
`2.69 s` cold earlier the same day — well inside the 10 s hook timeout.

### D6. Hook installer generalises to (event, matcher, command, recogniser)

`hooks.go` today hard-codes SessionStart and `isClioRecall`. It becomes a small table:

| event | matcher | command | recogniser |
|---|---|---|---|
| SessionStart | (none) | `<exe> recall` | `<…/clio> recall` |
| PreToolUse | `Read` | `<exe> file-history --hook` | `<…/clio> file-history --hook` |

The table is expressed as shared helpers parameterised by a recogniser
(`removeHookEntriesIn`, `hasHookEntry`, `groupsHaveWithMatcher`) rather than a data
structure; `AddClioHooks`/`RemoveClioHooks` apply both rows inside one `mutate`, so the
settings `.bak` is the pre-install file and a failure leaves neither hook half-registered
(two mutations would make the `.bak` the intermediate state — caught by the existing
backup test). Each entry is idempotent on its own, so a user who ran the old
`install-hook` gets only the missing PreToolUse entry on re-run; "present" for the
PreToolUse hook counts only inside a `matcher: "Read"` group (an entry a user moved under
another matcher would never fire for Read). `--no-file-context` also removes a previously
installed file-history hook (codex review), and removal drops an emptied `hooks` object;
the install message names only what actually changed. Removal keeps the
existing rule (`removeHookEntriesIn`): drop only clio's entries, keep a group that still
holds someone else's hook (a user's own `Read` gate can share the `matcher: "Read"`
group), and delete the event key only when nothing is left. `uninstall-hook` detects
clio's entries without a matcher restriction (`HasAnyClioHookEntries`) so a misplaced
entry is still cleaned up, while `install-hook`'s "already present" check is
matcher-scoped (verifier round 2 caught the asymmetry). `isClioRecall`'s
exact-shape rule (basename `clio`, exact arg list) is kept per recogniser so
`clio-helper file-history` never matches.

### D7. Recall tail = last `role='assistant'` row of the newest listed session, verbatim, rune-capped

`GetRecall` takes the excerpt from the digest's own first "Recent sessions" row, so the
two can never name different sessions (codex round 2 showed the since-bounded and
unbounded listings can nest subagents differently). Only when that since-bounded list is
empty does it run a second, **unbounded** `ListSessions` (same project prefix and page
size, no `since`) and use its first row — after a long break is exactly when "where did
we leave off" matters (codex round 1). The page size must match the digest's listing: a
`Limit: 1` page would promote every child whose parent is merely older. Note this is the
digest's page (`--limit`, default 5), not `clio list`'s default 50, so at a promotion
boundary the two commands can differ. One message query then takes the last non-blank
`role='assistant'` row; an undated row falls back to the session's `ended_at` for the
header. The listing and the message query are separate statements, not one snapshot: a
concurrent ingest between them is harmless for the common path (the excerpt's session
is already fixed) and only affects the fallback path by at most one newer session. `SELECT session_uuid, COALESCE(ts,0), content FROM messages WHERE session_uuid=? AND
role='assistant' AND TRIM(content) <> '' ORDER BY seq DESC LIMIT 1`. `Recall` gains
`LastAssistant *TailExcerpt{SessionUUID, TS, Text}`. `formatRecall` prints it first:

```
clio — recent activity in /Users/lin/Herd/clio
Last session left off (2026-09-06 19:13, a11f08b2):
  <up to 600 runes, whitespace-collapsed, "…" when cut>
Recent sessions:
  …
```

(`formatTS` prints date and minute; `shortID` is the 8-character prefix — same helpers
as the rest of the digest.)

600 runes ≈ 300–600 tokens; `--tail-runes 0` removes the section. Verbatim, not
sentence-aware: the closing message is usually already a summary (this repo's own sessions
end with a recap by convention), and any heuristic would be a generation policy in
disguise.

*Alternative*: last *user* message — rejected: it is usually a short instruction, not a
state description.

### D8. `<private>` is stripped at the top of `redactString`, before its JSON split

`stripPrivate` is a depth-tracking scan over `privateOpenRe` /
`privateCloseRe` (a self-closing `<private/>` is not an opener; the closer tolerates
trailing whitespace; an inner `<private>` never ends the outer element early — the first
version was a non-greedy regex, and the final whole-change verifier showed
`<private>alpha <private>beta</private> gamma</private>` leaked ` gamma`), called as the first statement of `redactString`, **not** inside `Redact`: `redactString`
splits its input at every `{`/`[` that parses as JSON and redacts the pieces separately,
so a private block containing JSON (the likely case: a pasted config)
would never be seen whole by a regex placed inside `Redact`. At `redactString`'s entry the
string is still intact. Every persisted text field reaches `redactString`: FTS content and titles
(`parser.go` `ParseLine`), tool summaries (`parser.go` `toolUseSummary`), target values
(`activity.go` `extractTargets`), and every string leaf of `raw_json` via `redactJSON` →
`redactWalk` (`redact.go` `redactWalkDepth`). The scan is block-local: a `<private>` that opens in one content block
and closes in another is not recognised (each block is its own string), and the spec says
so. `redactJSON`'s decode-failure fallback bypasses `redactString`; the
implementation also calls `stripPrivate` there so the invariant holds without relying on
the parser's earlier `json.Unmarshal` guard. Non-greedy `.*?` gives "two blocks →
two spans"; an unterminated opener does not match and the text is left intact (spec
scenario). Ordering before secret redaction means a secret inside a private block simply
disappears rather than leaving a `[REDACTED:…]` marker that reveals a secret was there.

*Alternative*: strip at the parser (`parser.go` text path) — rejected: misses `raw_json`
and every future caller; the redaction funnel is the single choke point.

### D9. Claude-Code-only, like recall (hook and digest only; `<private>` applies to every source)

Both the digest and the timeline pass the default (`""`) source to `db.SourceFilter`,
matching `GetRecall`'s stated policy (its "Claude-Code-only by policy" comment). Codex/Gemini sessions never
inject into a Claude Code session.

## Risks / Trade-offs

- [Every `Read` spawns a process] → read-only open, two indexed queries, `timeout: 10`,
  measured 10–20 ms warm (D5); users can opt out with `--no-file-context` or `uninstall-hook`.
- [Cold first run ~3 s once per boot] → accepted; it is the same cost `recall` already
  pays at session start and it is bounded by the hook timeout.
- [Staleness gate hides history after any out-of-band edit] → documented; open question
  whether to annotate instead of skip.
- [Tail excerpt can carry a `[REDACTED:…]` marker or an interrupted message] → it is
  verbatim by design; the section header makes provenance clear.
- [The excerpt re-injects verbatim assistant text at every session start, and that text
  may be quoting external content (a web page, someone else's README, a PR body) the
  previous session was discussing] → same trust class as the transcript itself, but new
  as an *automatic* injection surface; README/USAGE (tasks 5.1/5.2) must say the section
  is the previous session's raw closing text, not a curated summary, and `--tail-runes 0`
  turns it off. Raised by the group-2 verifier.
- [With another session of the same project still running, the "newest" session is that
  live one and the excerpt is its latest mid-work sentence, not a closing recap] →
  accepted (observed on the live index while implementing group 2); the header shows
  the session id and time so the reader can tell. On `resume` the newest session is the
  resumed one itself, which is the useful case.
- [Extra ~300–600 tokens per session start] → `--tail-runes` flag; default chosen to fit
  one closing recap.
- [`<private>` in a *tool result* (e.g. Claude reads a file containing the tag)] → stripped
  too, since the walk covers string leaves; this is the conservative direction and is
  called out in the README.
- [`<private>` spanning two content blocks, or written in a form the regex does not see
  (HTML-escaped, split by markdown)] → not stripped; README states the supported form is a
  literal open/close pair inside one message.
- [`toolUseSummary` now runs `redactString` on the whole tool input instead of its first
  200 bytes, and `redactString` probes a JSON decoder at every `{`/`[`, which is
  near-quadratic on bracket floods (measured: 100k `[` → ~10.8 s)] → pre-existing on the
  `raw_json` path, which already ran the same string through `redactString` as a JSON
  leaf (measured ~10.3 s for the same input), so the change doubles a stall that already
  existed rather than creating one; recorded as a `debt:` comment at the call site and
  as a follow-up (bound the probe count per string). Raised by the codex review.
- [A `<private>` that spans a tool_result's nested content blocks is stripped from the
  joined `content` but survives in `raw_json`] → documented asymmetry of an unsupported
  form (spec scenario); consistent handling would need per-block stripping in
  `toolResultText`, which the raw_json walk cannot mirror. Raised by the codex review.
- [Tool-input summaries were truncated *before* redaction (`parser.go` `toolUseSummary`),
  so a private block longer than 200 bytes lost its closer and leaked its head into
  `messages.content` / FTS] → order swapped to redact-then-truncate, matching
  `codex.go`'s existing `firstLine(redactString(value), 200)`; found by the group-1
  verifier's value-domain probe.
- [Users on the old `install-hook` do not get the PreToolUse hook automatically] → the
  re-run path is idempotent and the changelog says to re-run.
- [Hook payload sniffing across Claude Code versions] → only `tool_name`,
  `tool_input.file_path`, `session_id` are read; any missing or mistyped field → empty
  output. A Read carrying only a `filePaths` array (batch form) is not supported and
  stays silent; payloads over the 64 KB `readStdinIfPiped` cap are truncated and fail
  JSON parsing → silent (a Read payload is far smaller in practice).
- [Hook commands embed `os.Executable()` unquoted; a binary path containing a space
  would split at install time and never match the recogniser] → pre-existing for the
  recall hook (same `exe + " recall"` shape, same exact-shape recogniser); fixing it means
  changing both recognisers together, left as `debt:` for a follow-up change.
- [`mutate` rewrites `.bak` on every successful content change, so a second
  `install-hook` overwrites the pre-install backup with the first install's state] →
  pre-existing `claudeconfig` semantics, unchanged here; the changelog note says to keep a
  copy if the first backup matters.
- [Digest header prints the payload's path verbatim while the lookup uses the
  normalised key (`..` segments, redacted fragments)] → cosmetic; the rows are correct.

## Migration Plan

- No schema migration. Ship in the next release with a changelog note: re-run
  `clio install-hook` to add the file-context hook; run `clio index --full` if you want
  `<private>` stripping applied to history.
- Rollback: `clio uninstall-hook` removes both hooks; the `<private>` rule is ingest-only
  and reverts with the binary (re-index restores the text, since the source JSONL is
  untouched).

## Open Questions

0. **Import direction for `NormalizeTargetPath` (found while implementing group 1)**:
   `internal/ingest`'s product code imports only `config`/`db`/`model` (`go list -f
   '{{.Imports}}'`), but its *internal* test package imports `internal/sessions`
   (`codex_activity_test.go:11`), so `sessions` → `ingest` would be a test-time cycle
   ("import cycle not allowed in test"). That test file uses unexported symbols (`codexSource`, `openTestDB`), so moving it
   to `ingest_test` is not free. **Resolved at group 3**: the helper lives in `ingest`
   (`ingest.NormalizeTargetPath`, next to `extractTargets`, with a test that it equals
   the stored value) and the CLI normalises the key before calling
   `sessions.FileHistory`; `sessions` stays free of ingest imports.

1. Staleness gate: skip (current spec) vs. show with a "changed since" line.
2. Should the timeline also list *sessions that only read* the file, or edits only? The
   spec lists all touches with the tool set; a `--edits-only` flag is a cheap follow-up.
3. Tail excerpt default 600 runes — revisit after a week of real session starts.
4. Whether `install-hook` should print a one-line note when it adds the PreToolUse hook to
   an existing SessionStart-only setup (leaning yes; cosmetic).
