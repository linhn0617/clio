# Changelog

All notable changes to clio are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Harness text no longer indexed as user text** (openspec change
  `strip-harness-tags`). Claude Code writes `<system-reminder>`,
  `<task-notification>`, relayed `<agent-message>` and `<local-command-caveat>`
  elements into user-role messages; `search`, `ask` and `recall` matched them as
  things the user said (on a live index, 468 of 3,839 user messages carried a task
  notification, 299 a system reminder, 187 an agent message). Ingest now removes
  each complete element, plus an agent message's fixed preamble and trailer lines,
  from user text; a message that was nothing else is not stored, and a prompt with
  an appended reminder keeps the prompt as its text and title. A caveat-only
  message, which was already skipped for the title but still stored, is no longer
  stored; command records (`<command-name>`, `<local-command-stdout>`, `<bash-*>`)
  stay. Only line-anchored, properly closed elements are removed, so a tag
  mentioned in prose or left unclosed stays. `raw_json`, assistant text, tool
  results and Codex sessions are unchanged. Run `clio index --full` after upgrading
  to strip them from sessions already indexed.

## [0.21.0] - 2026-10-19

Bounded MCP responses, a faster and wider secret redaction, cheaper read-path
catch-up, and `clio doctor --quick` with Claude Code integration checks (openspec
changes `mcp-response-budget`, `redaction-prefilter`, `redaction-rules-linear`,
`ingest-catchup-cost`, `mcp-startup-lease`, `doctor-quick-integration`).

### Added

- **MCP response budget.** Every tool takes `max_chars` (default 40,000, range
  4,000–48,000, counted in UTF-16 code units) so a result stays under Claude Code's
  50,000-character spill-to-file threshold. Rows past the budget are dropped at a
  row boundary, a single oversized row has its text cut with a
  `… [truncated N chars]` marker, and the response carries `truncated: true` and a
  `next` hint. `ask` keeps hit passages first; `read_session` shrinks its parent page
  before shedding inlined subagent messages. Results are sent as text only.
- **`read_session` `last_n` and `around_sequence`** read the end of a session or a
  window centred on a message `seq`, in one read snapshot; the response reports
  `total`.
- **`clio doctor --quick`** skips the whole-database scans (db integrity, the FTS
  content check, FTS bloat); FTS row counts and every other check still run.
- **Claude Code integration checks in `clio doctor`:** recall and file-history hooks
  each installed and pointing at a real binary (an unquoted path with whitespace or
  the old `Read` matcher says to rerun `clio install-hook`), MCP registration, the
  MCP lease, and index freshness judged from source reconciliation (a session that
  stopped mid-line is not drift).
- **New secret rules:** GitLab `glpat-`, npm `npm_`, Hugging Face `hf_`, Stripe
  `whsec_`, Slack incoming-webhook URLs, PGP private key blocks,
  `Authorization: token <x>`, empty-user connection strings (`redis://:pass@host`),
  `PASSPHRASE` and credential-context `*_PASS` / `*_PWD` keys (`DB_PASS`,
  `MY_DB_PASS`; not `FIRST_PASS` or the shell's `OLD_PWD`).

### Changed

- **Redaction is faster with identical output.** Each rule carries a literal
  prefilter and is skipped when its feature is absent (about 2× on a full index,
  ~5× on CJK-heavy text); a tool call's summary and activity target share one pass.
- **Linear-time `<private>` stripping and JSON probing:** pathological nesting and
  bracket floods no longer stall ingest.
- **Cheaper read-path catch-up.** Migration 0016 adds an activity-backfill watermark,
  so `list`/`search`/`ask` check only messages newer than it; `clio index` still
  repairs the full history. Migration 0015 drops the redundant `messages(session_uuid)`
  index.
- **`clio mcp` leader startup** answers immediately, runs one catch-up, and renews its
  lease throughout, so a long rebuild no longer lets another process take over.

### Fixed

- Redaction gaps: a connection string containing `sk-` left a stray `]`; `x_sk-…`
  keys; keys after a non-ASCII fold (`ſecret=`); a key value that swallowed a glued
  second key no longer exposes the second value; a token inside a `Bearer` value no
  longer leaves the bearer head visible.

## [0.20.0] - 2026-10-17

The file-context hook now also fires on Bash (openspec change
`bash-file-context-hook`). Most file reads happen through `cat`/`sed`/`grep`
rather than the Read tool, so until now the hook rarely had a chance to speak.

### Added

- **Bash file context.** `clio file-history --hook` handles `tool_name = Bash`:
  it scans the command's shell words, keeps the first three that name an
  existing regular file under the session's working directory (symlinks
  resolved; heredoc bodies, comments, redirect and `tee` targets and flags are
  skipped; at most 64 words are examined) and injects their past-session
  timelines. Commands that name no file cost nothing beyond process start — the
  index is not opened. Several files share the 8,000-character budget.
- **`internal/shellwords`** — one shell word scanner shared by the hook and
  the hook recogniser in `install-hook`.

### Changed

- **`clio install-hook` registers the file-history hook with matcher
  `Read|Bash`.** An older install under matcher `Read` is upgraded in place
  (a group shared with foreign hooks keeps them where they are). Matchers are
  read by Claude Code's documented rules (`*`/empty = every tool, exact
  `|`/`,` lists, anything else an unanchored regex); the hook counts as
  installed only under a matcher that covers both Read and Bash, other
  placements print a warning instead of adding a second group, and duplicate
  entries are collapsed. The command now reports what it actually changed.

## [0.19.0] - 2026-10-17

Removes the Gemini CLI source (openspec change `remove-gemini-cli-source`).
Google retired Gemini CLI's personal tier upstream — a live request now answers
`This client is no longer supported for Gemini Code Assist for individuals …
migrate to the Antigravity suite` — so the adapter had no working target.

### Removed

- **Gemini CLI source adapter.** `~/.gemini/tmp/*/logs.json` is no longer
  scanned; `gemini` is gone from `--source`, the MCP `source` enums, the TUI
  source label and the `doctor` root report. `clio --source gemini` is rejected
  as an invalid source.
- The orphaned `errUnusableStateRecord` abort path in the ingest loop (only the
  Gemini adapter ever raised it). The generic `WholeFileReplay()` source SPI
  stays.

### Changed

- **Migration 0014 deletes previously indexed Gemini data — this is
  irreversible.** On the next command that opens the database for writing, all
  rows with `source = 'gemini'` are removed from `sessions`, `messages` (the FTS
  index follows via trigger), `tool_calls`, `tool_targets`, `session_usage`,
  `quota_snapshots`, `ingest_state` and `source_conflicts`. Back up
  `db.sqlite` first if you want to keep those sessions.

A future Antigravity CLI adapter would be a separate change.

## [0.18.0] - 2026-10-17

Hardening pass from a six-way review of the shipped code (openspec change
`2026-10-16-review-hardening`): sessions survive Claude Code's transcript
cleanup, the FTS index is kept compact, and every human-readable surface is
terminal-safe.

### Added

- **`clio index --purge-missing [--force]`** — the only command that now drops
  sessions whose transcript is gone. It refuses when more than ten files vanished
  together within 24 hours and that cluster is over half the candidates (an
  unmounted disk looks exactly like that); `--force` overrides. Refused while
  `clio mcp` holds the index lock.
- **`clio doctor`** reports `retained sessions` (kept after their transcript was
  deleted) and `fts bloat` (FTS size vs message text from `dbstat`; red when the
  ratio exceeds 2.0 and the FTS index is at least 16 MB).
- **`internal/termsafe`** — terminal escape sequences (CSI/OSC/DCS/APC/PM/SOS,
  C0 controls except newline and tab, DEL, raw and UTF-8 C1) are stripped from
  `list`, `show`, `subagent`, `activity`, `recall`, `file-history`, `usage`
  (including `--quota`) and every TUI pane. `--json`, raw events and MCP output
  are unchanged.

### Changed

- **Deleted transcripts no longer delete sessions.** Claude Code removes
  transcripts after `cleanupPeriodDays` (30 by default); until now the next
  `clio index` dropped those sessions too. The catch-up now only marks
  `ingest_state.source_missing_at` (migration 0013, cleared if the file comes
  back); a live copy of the same session uuid repoints the session instead of
  purging it. The README documents the Claude Code setting.
- **FTS maintenance.** `clio index --full` runs `optimize` on the FTS index
  after purging, and `clio prune-raw --vacuum` optimizes before it vacuums. A
  never-optimized index was measured at 3.8× its message text; optimize plus
  vacuum brought a 3.4 GB file to 948 MB.
- **Redaction** now applies to JSON object keys as well as values, so a token
  used as a key is masked like one used as a value; two keys that redact to the
  same name collapse to a single `[REDACTED:key]` entry.
- **`clio list`** hides subagent sessions whenever their parent passes the same
  filters, not only when the parent lands on the same page; a child whose parent
  is filtered out or missing is still promoted.
- **`clio recall`** excludes the calling session (and its subagents) from the
  digest using the hook payload's `session_id`; `--project` matches on a path
  boundary and is case-sensitive, so `/a/b` no longer matches `/a/bc`; touched
  files are limited to the project root; output is capped at 9,500 characters
  (commands, then files, then sessions, then the excerpt give way). `clio
  install-hook` writes the SessionStart hook with
  `matcher: startup|resume|clear|compact` and `timeout: 10`, and upgrades an
  older entry in place.
- **`clio install-hook`** shell-quotes the executable path, so a `clio` under a
  directory containing spaces works; an older unquoted entry is recognised,
  upgraded and removable by `uninstall-hook`. A `settings.json` symlink is
  resolved first, so the lock, temp file and `.bak` sit next to the real file.

## [0.17.0] - 2026-10-16

Fix release: closes a `<private>` redaction gap found on a live index, and takes
the pending dependency bumps (Go 1.26, mcp-go 1.x).

### Fixed

- **`<private>` stripping** — a lone `<private>` earlier in a message no longer
  shields a later complete `<private>…</private>`: the stray opener stays as
  literal text and the complete element is still removed (#47). On a live index
  35 messages still carried a complete element after 0.16.0, 32 of them in this
  shape. Run `clio index --full` after upgrading to strip them.
- **`clio mcp` under mcp-go 1.x** — mcp-go 1.x runs tool calls on a worker pool
  and cancels in-flight requests as soon as stdin reaches EOF. `Serve` now pins
  the pool to a single worker: every tool starts with a best-effort catch-up
  ingest, so concurrent calls would only contend for the SQLite write lock. The
  stdio e2e test keeps stdin open until all responses have arrived (#44).

### Changed

- Dependencies: modernc.org/sqlite 1.57.0 → 1.60.1 (#43), mark3labs/mcp-go
  0.58.0 → 1.2.1 (#44), mattn/go-runewidth 0.0.28 → 0.0.31 (#45). CI builds
  and tests with Go 1.26 (#46).

### Notes

- `go.mod` now declares `go 1.26.0` (required by sqlite 1.60.1). `go install
  …@v0.17.0` on an older toolchain fetches Go 1.26 automatically under the
  default `GOTOOLCHAIN=auto`; prebuilt release assets are unaffected.

## [0.16.0] - 2026-09-06

Three designs borrowed from a survey of claude-mem that give Claude cross-session
context without any LLM call, daemon, vector store or network access (openspec
change `add-file-context-hook-recall-tail-private-tag`).

### Added

- **`clio file-history <path>`** — the Claude Code sessions whose tool calls read
  or edited one file, newest first, with the tools each used (exact path match on
  the indexed `tool_targets`; subagent touches roll up to the parent). Silent
  (exit 0) when there is no history or no index.
- **PreToolUse(Read) hook** — `clio file-history --hook` reads the hook payload
  from stdin and returns the file's timeline as `additionalContext`, so Claude
  sees who touched a file before reading it. Guard rails: skipped when the file
  on disk is newer than its newest indexed touch, the calling session's own
  touches are excluded, output capped at 8,000 characters, 10-second hook
  timeout, and every failure path is silent so a Read is never blocked.
- **`<private>…</private>` opt-out at ingest** — the whole element, content
  included, is removed from searchable text, stored raw events, tool summaries
  and session titles across all sources. Block-local (one open/close pair inside
  one message); `<private/>` is not an opener. Sessions indexed by an older
  clio keep their text until `clio index --full`.

### Changed

- **`clio recall`** now opens with "Last session left off": the previous
  session's final assistant message, verbatim, bounded by `--tail-runes`
  (default 600, `0` omits). The excerpt follows the first row of the digest's
  own session list and, when the project has been idle longer than `--since`,
  falls back to the project's newest session so a long break still starts with
  context.
- **`clio install-hook`** registers both hooks in one atomic settings write
  (`settings.json.bak` is the pre-install file); `--no-file-context` keeps only
  the recall hook and removes a file-history hook installed earlier;
  `uninstall-hook` removes both. Users of an older `install-hook` should re-run
  it to add the file-history hook.
- Tool-input summaries are now redacted before their 200-byte truncation
  (matching the Codex adapter), so a long secret or `<private>` block can no
  longer leave its head in the summary.

## [0.15.0] - 2026-07-26

Reclaim disk space: `clio prune-raw` blanks stored raw_json for old sessions as
a reversible cache eviction. Spec-driven and adversarially cross-model reviewed
(8 rounds to GO on the spec, 7 rounds to a clean gate on the implementation).

### Added

- **`clio prune-raw --older-than <dur> [--dry-run] [--vacuum]`** — blanks
  `messages.raw_json` (used only by `clio show --format raw|json`) for sessions
  older than the cutoff whose raw form is **restorable** from the still-present
  source file. This is a reversible cache eviction: `clio index --full`
  re-derives it. Search, `content`, token usage, quota, and activity data end
  byte-identical. Only demonstrably-restorable sessions are pruned (indexed
  snapshot matches the file: size+mtime+offset, and a verified — not aborted —
  ingest); anything unverifiable is skipped and counted by reason. `--dry-run`
  reports without writing; `--vacuum` shrinks the file on disk (refuses under
  the MCP lock). No monetary/token data is touched.
- `clio show --format raw|json` on a pruned session degrades honestly — a
  per-session note (raw) or `RawJSON: null` + `raw_pruned: true` (json) — instead
  of blank output; `--format markdown` is unaffected.
- `clio doctor` reports pruned-message counts and reclaimable-on-VACUUM bytes.

### Changed

- Migration 0012 narrows the `messages` update trigger to `AFTER UPDATE OF
  content`, so pruning raw_json does not churn the FTS trigram index, and adds
  an `ingest_state.aborted` signal (fail-closed on upgrade) that gates prune
  restorability.
- `clio index --full` now exits non-zero if any pruned session could not be
  restored (e.g. its source file became unreadable), so an unrestored prune is
  never silent.

### Notes

- Prune is reversible only for sessions clio can rediscover and re-parse; after
  upgrading, a session becomes prunable once a `clio index --full` has
  re-validated it (existing ingest rows are fail-closed until then).
- Space is freed inside the DB immediately but the file shrinks on disk only
  with `--vacuum` (or a later `VACUUM`).

## [0.14.0] - 2026-07-23

Session-level token usage: see which sessions burned your tokens, and jump
straight into them. Spec-driven and adversarially cross-model reviewed
(7 rounds to GO on the spec, 4 rounds to a clean gate on the implementation).

### Added

- **`clio usage`** — token usage by session, project, or model, sectioned
  **per source** (token counts from different tools use different tokenizers
  and are never combined into a cross-source total). `--by session` rows carry
  a uuid you can pass to `clio show`; project/model rows carry a copy-pasteable
  (shell-safe) drill-down command; `--model` filters to one model. Raw token
  counts only — clio never stores, computes, or displays monetary amounts.
- **`clio usage --quota`** — last-observed rate-limit snapshots extracted from
  session files (currently Codex persists these). Never live data: every line
  shows its observation age, and snapshots older than their window (or past
  their reset time) render as STALE. Quota data is CLI-only and never exposed
  over MCP.
- **Usage extraction in ingest** for all three sources: Claude Code (dedicated
  whole-file usage pass with literal last-wins uuid dedup — the filtered
  message index is never used as the usage source), Codex (latest cumulative
  `token_count`, model always `(mixed)`), Gemini (per-message tokens summed
  during replay, per-model). Aggregates are stored per (session, model) in the
  new `session_usage` table.
- Token totals in `clio list` (text + `--json`) and the TUI browse/activity
  views; MCP `activity_summary` gains `group_by: "usage"` (token aggregates
  only). A `[stale]` marker — propagated to group rows and subtotals — means
  the file's last usage scan failed and values are last-known, not current.
- `clio doctor` reports usage coverage, skipped/unmapped usage diagnostics,
  and stale-usage file counts.

### Changed

- `clio index --full` now **refuses with a non-zero exit** when the clio MCP
  server holds the index lock, instead of reporting a silent "nothing to do"
  success — a requested full rebuild must never be skipped silently.

### Notes

- **Backfill required for existing indexes:** usage for sessions indexed
  before v0.14.0 appears after a one-time `clio index --full` (stop Claude
  Code / the clio MCP server first).
- Performance (certified, fail-closed harness in the archived change dir):
  DB growth +0.1%, full-index wall-clock -0.2%, tail-ingest +9.98 ms absolute
  on a 5,000-message session, write-lock hold change noise-level (-1.4%).

## [0.13.0] - 2026-07-20

Gemini CLI history support. Spec-first, adversarially reviewed, and — for the
op-log format — confirmed against a real captured session before shipping.

### Added

- **Gemini CLI as a third source.** clio now indexes Gemini CLI conversation
  history (`~/.gemini/tmp/<project>/chats/*.jsonl`, the ChatRecordingService
  format) alongside Claude Code and Codex. Use `--source gemini` on any command
  or `source: "gemini"` on the MCP tools; it's wired through a single source
  registry so every surface (CLI, MCP, doctor, TUI) picks it up.
- The registry refactor behind this: source names, labels, root availability,
  and the CLI/MCP/doctor/TUI derivations now come from one static-seeded list
  instead of per-surface hardcoded `{claude-code, codex}` literals.

### Notes

- Gemini files are an op-log: a `$set` sets full state, individual turns arrive
  as bare records upserted by id, and the whole file is re-read on any change
  (idempotent). This was corrected against a real assistant-bearing session —
  the initial schema, inferred from source types, was wrong.
- Scoped v1: conversation **text** is indexed and searchable. Structured
  extraction of Gemini's `thoughts` / `toolCalls`, `$rewindTo` rewind handling,
  and subagent parent-linking are follow-ups; that detail still lives in the
  stored raw event (`clio show --format raw`) in the meantime.
- Heads-up for Gemini CLI users: Google ended the free "Sign in with Google"
  auth for Gemini CLI on 2026-06-18 (migrating individuals to Antigravity). Your
  existing `chats/` history is still on disk and indexable; new sessions need
  API-key or enterprise auth. clio does not care which — it reads the local
  files.

## [0.12.0] - 2026-07-15

Token-budgeted evidence bundles, an assertion-based retrieval regression
suite, and content-level FTS index verification. Both features were
spec'd first (openspec), adversarially reviewed, and implemented against
the revised specs.

### Added

- `ask` evidence bundles are bounded by a global token budget: a
  deterministic, dependency-free estimator (CJK runes 1:1, other runes
  ~4:1) enforces an effective bound of max(budget, the top group's
  minimum-length hit excerpts). Whole lower-ranked groups are dropped
  first and a group is never partially emitted — except the top group,
  whose hit excerpts always survive, so a tiny budget returns a trimmed
  bundle rather than an empty one. Exposed as `max_tokens` on the MCP
  `ask` tool and `--max-tokens` on `clio ask` (default 2000, clamped to
  200–8000 on every surface).
- A retrieval regression suite (`internal/eval`): 12 synthetic bilingual
  sessions and 20 assertion-style queries (EN / CJK / mixed / code
  fragments / quoted phrases / FTS-vs-LIKE tiers) pinning search and ask
  top-k behavior, snippet visibility, and budget compliance. Runs as
  plain `go test` inside the existing CI step; failures name the query,
  the expectation, and the actual top-k.
- `clio doctor` verifies FTS index content, not just row counts: when
  counts match, it runs SQLite's FTS5 `integrity-check`, catching
  content-level corruption the old check silently passed (~0.5s on a
  50k-message database).

### Notes

- The openspec source-adapter registry proposal
  (`openspec/changes/2026-07-14-generalize-source-adapter-spi/`) ships as
  a reviewed spec only; implementation is deferred.

## [0.11.0] - 2026-07-14

Retrieval-quality and diagnostics release, driven by an adversarial
cross-model review (GPT) of the v0.10.0 audit: readable snippets on both
query paths, honest doctor verdicts, lossless config rewrites, clean
session titles, and per-source evidence attribution.

### Fixed

- `clio doctor` no longer under-reports: reconciliation lag (unindexed
  bytes) and detected rewrites now fail the check; same-size rewrites
  (equal size, changed mtime) are detected; coverage is compared per
  source, so Codex rows can't mask a Claude gap; a Codex-only install is
  no longer warned about the missing Claude projects directory.
- Config rewrites (`install-mcp` / `install-hook` / `uninstall-*`)
  preserve numeric fidelity: integers above 2^53 and high-precision
  decimals round-trip byte-exact instead of being silently rounded
  through float64. (Alphabetical key reordering remains and is now a
  documented known limitation.)
- FTS search snippets widen from 10 trigram tokens (~12 chars) to 64,
  and the short-query LIKE fallback now windows around the first match
  (CJK-safe) instead of always returning the first 160 characters —
  which could omit the matched term entirely.
- `clio ask` no longer merges terms into an unmatchable phrase when the
  question contains an embedded unmatched double quote (common when
  asking about code): terms pass to retrieval as a structured slice.
- Session titles skip Claude Code harness boilerplate
  (`<local-command-caveat>`, `<task-notification>`, `[Request
  interrupted`) and use the first substantive user message instead.
  Existing sessions pick up cleaned titles on the next
  `clio index --full`.
- `clio tui --source codex|all` now applies the source filter in the
  Ask tab (it silently queried claude-code before), and the tab bar and
  input headers no longer overflow narrow terminals.

### Added

- Evidence groups from `ask` carry their source; the MCP `ask` response
  always includes it and the CLI shows a `[source]` tag under
  `--source all`, matching `search`.

## [0.10.0] - 2026-07-08

Hardening release from a full-repo review: two correctness fixes, a persistent
config backup, install-command test coverage, and CI/release gates.

### Added

- Persistent `.bak` recovery point: after `install-mcp` / `install-hook` /
  `uninstall-*` successfully changes `~/.claude.json` or settings, the previous
  version is kept as `<file>.bak` (matching permissions) for manual recovery.
  No-op reruns don't overwrite an existing backup; failed writes leave no
  misleading one.
- Test coverage for the install commands' config mutations: existing config
  preserved, idempotent reruns, write-failure safety, malformed JSON refused,
  uninstall removes only clio's entries.
- Dependabot (gomod + github-actions, weekly).

### Fixed

- `activity_summary` (MCP) silently ignored its `project` filter for the
  `day` (default) and `project` groupings — it now filters by project-path
  prefix like the other groupings.
- A Codex rollout file whose `session_meta.id` disagreed with the filename
  uuid was rejected and never indexed (with the watcher re-logging the error
  every 60s); it now warns once and indexes under the filename uuid.

### Changed

- Built-in ingest sources are registered from a single composition root
  (`NewWithBuiltinSources`) instead of at every entry point, so a missed
  call site can no longer silently drop a source.
- CI runs the race detector; releases run vet+test before building.
- Privacy docs now state redaction is pattern-based and best-effort, and that
  registering the MCP server grants access to the entire indexed history.

## [0.9.1] - 2026-06-21

Follow-up to v0.9.0: Codex tool activity is now extracted, so the activity surfaces
work for Codex the way they already do for Claude Code.

### Added

- Codex activity targets: `function_call` records now produce structured `command`,
  `file`, and `tool` activity facts, so `clio activity --by command|file|tool --source
  codex` and `clio list --ran/--touched --source codex` surface Codex work.
  `exec_command` and `shell` (the `bash -lc` script) become commands, `view_image`
  becomes a file. Codex tool-use rows also now show the actual command in `clio show`
  and full-text search, instead of only the tool name.

### Notes

- Existing Codex sessions indexed by v0.9.0 gain these targets on the next
  `clio index --full` (a full reindex re-derives activity for already-indexed files).
- clio still never indexes its own MCP traffic, and command/file values are
  secret-redacted before storage. `clio recall` stays Claude-Code-only by design.
- Codex edits made through an `apply_patch` heredoc inside a shell command are recorded
  as the command (not yet as individual file targets); `--by file --source codex`
  currently reflects `view_image` paths.

## [0.9.0] - 2026-06-19

clio now indexes **OpenAI Codex CLI** history (`~/.codex/sessions`) alongside Claude
Code, behind an opt-in `--source` filter. Ingestion became pluggable, so default
behavior is unchanged — every command stays Claude-Code-only until you ask for more
with `--source codex` or `--source all`.

### Added

- Codex CLI ingestion: append-only rollout transcripts under `~/.codex/sessions` are
  indexed as `codex` sessions (project path from the real `cwd`; the API `response_item`
  stream is authoritative, the duplicate UI event stream skipped, harness wrapper blocks
  stripped). `clio index` and the MCP server pick them up automatically when Codex is
  installed — including on Codex-only machines with no `~/.claude/projects`.
- `--source claude-code|codex|all` (default `claude-code`) on `clio search`, `list`,
  `show`, `ask`, `activity`, and `clio tui` (launch flag + `[codex]` row labels); the
  five MCP read tools gain a matching `source` parameter, and results carry their source.
- A `source` column on sessions; cross-source UUID collisions fail closed — detected
  inside the ingest transaction, refused without writing rows, and recorded for
  `clio doctor`.

### Changed

- The ingester is now a pluggable source-adapter SPI: Claude Code ingestion is one
  adapter (behavior unchanged), Codex another. The file watcher live-watches every
  source root, and deletion reconciliation is partitioned per root so a
  temporarily-unavailable source can't purge its index. `clio doctor` is source-aware.

### Notes

- `clio recall` stays Claude-Code-only by design. Codex activity breakdowns
  (`clio activity --by command --source codex`, `--ran`/`--touched` over Codex) are not
  extracted yet — a planned follow-up.

## [0.8.0] - 2026-06-19

Claude Code runs subagents (the Task tool) and stores each one's transcript in its
own file. clio now recognizes those transcripts and links them to the conversation
that spawned them, instead of indexing them as disconnected `agent-…` sessions. Your
session list is no longer cluttered by them; they nest under their parent, stay
searchable (labeled as subagents), and are reachable from the parent in every surface.

### Added

- Subagent ingestion: a transcript under a `subagents/` directory is linked to its
  parent session (`parent_session`) and tagged with its type (e.g. `general-purpose`,
  `Explore`). Existing histories are relinked automatically on the next index (a
  one-time backfill); no full re-index needed.
- `clio list` hides subagent children by default and annotates a parent with
  `(+N subagents)`; `--include-subagents` lists them. `clio show <parent>` lists a
  session's subagents and inlines them with `--include-subagents`; `clio show
  <agent-id>` shows the subagent with its parent and type. `clio search` labels a hit
  that comes from a subagent.
- MCP: `list_sessions` excludes subagents by default (`include_subagents` to include),
  `read_session` reports a parent's subagents (and inlines them with
  `include_subagents`), and `search` results carry `parent_session`/`agent_type`.
- TUI Browse nests subagents under their parent (`Enter` expands/collapses); Search
  marks subagent hits.

### Changed

- `sessions.ActivitySummary` counts a parent and its subagents as one session (while
  their messages still count). (internal)

## [0.7.0] - 2026-06-16

Browse and search your history without leaving the terminal. `clio tui` opens a
full-screen dashboard over the same index the CLI and MCP server read — live
search, recent sessions, an activity overview, and question-answering — each with
a master-detail preview of the matched conversation. Read-only: nothing is written
to the index while you browse.

### Added

- `clio tui`: an interactive Bubble Tea dashboard with four tabs — **Search**
  (debounced live search; the selected hit is windowed and highlighted in its
  surrounding turns), **Browse** (recent sessions, optional `--project`), **Activity**
  (top files / commands / tools, drilling into the sessions behind each entry), and
  **Ask** (`ask` evidence groups with their windowed excerpts). `Tab`/`Shift-Tab` or
  `1`-`4` switch tabs; `↑↓` / `j k` navigate (lists scroll to keep the selection in
  view); `Esc` or `Ctrl-C` quits. On Search and Ask the focused input takes `q` and
  the digits as query text. Opens like `search` (incremental catch-up; defers to a
  running MCP server) and, when no index exists, exits with a hint to run `clio index`.

### Changed

- `sessions.GetMessages` / `GetWindow` take an `includeRaw` flag so the TUI preview
  skips the `raw_json` column it never renders, keeping selection-driven loads light
  on a large index. (internal)

## [0.6.0] - 2026-06-14

Ask a question, get a cited answer from your own history. `clio ask` retrieves the
conversation excerpts most relevant to a question — each windowed in its
surrounding turns and grouped by session — for you (or Claude over MCP) to
synthesize from. clio generates nothing and makes no network call; retrieval stays
local.

### Added

- `clio ask "<question>"`: a retrieval-only, cited evidence bundle over indexed
  history. It extracts content terms (bilingual; an unspaced CJK question expands
  to trigrams for the FTS index plus bigrams for the substring fallback, split on
  stopwords), retrieves any-term matches with full-term (FTS) hits ranked ahead of
  substring-only (LIKE) hits, groups them by session, and windows each hit in its
  surrounding user/assistant turns. Flags: `--project` (default all projects),
  `--since`, `--limit`, `--window`, `--json`. Reads the index like `search`
  (incremental catch-up; defers to a running MCP server).
- MCP: an `ask` tool returning the same bundle as structured JSON, so Claude
  synthesizes the answer from grounded excerpts and cites session ids. Read-only.

## [0.5.0] - 2026-06-13

Ambient recall: an opt-in SessionStart hook that opens each new Claude Code
session with a digest of what recently happened in the project — so past work is
surfaced proactively, without Claude having to think to search for it first.

### Added

- `clio recall`: a read-only, project-scoped digest of recent activity — the
  project's most recent sessions (title, date, turns), the files it recently
  touched, and the commands it recently ran. It detects the project from the
  working directory (walking up to the repo root), opens the index read-only (no
  ingest, no write-lock contention with a running MCP server), prints nothing
  when the project has no indexed history, and exits 0 with empty output on any
  error so it can never break session startup.
- `clio install-hook` / `clio uninstall-hook`: opt-in, atomic registration of a
  Claude Code SessionStart hook (in `~/.claude/settings.json`) that runs
  `clio recall`. Preserves your existing hooks (removes only clio's own entry on
  uninstall) and is separate from `install-mcp`.

## [0.4.0] - 2026-06-13

An activity index over your tool calls: search and summarize which files past
sessions touched, which commands they ran, and which tools they used. The database
migration and a one-time backfill apply automatically on next run; no forced reindex.

### Added

- Activity index. At ingest, each tool call records structured facts — files
  touched (Edit/Write/Read/NotebookEdit/MultiEdit), commands run (Bash), search
  patterns (Grep/Glob), fetched URLs (WebFetch), and the tool used (including MCP
  servers). Existing history is backfilled automatically from stored events,
  without re-reading source files.
- CLI: `clio activity --by file|command|tool|pattern|url` summaries, plus
  `--touched <path>`, `--tool <name>`, and `--ran <substring>` filters on
  `clio list` and `clio search`.
- MCP: `list_sessions` gains `touched`/`tool`/`ran` filters, and `activity_summary`
  `group_by` gains `file|command|tool|pattern|url`, so Claude can answer "which
  conversations touched `auth.ts`?" or "what commands did I run last week?".
- README: a "Why not just `grep`?" section, and the `search` tool now documents its
  recency-aware ranking.

## [0.3.0] - 2026-05-25

Privacy hardening and more reliable indexing. Backward-compatible; the database
migration applies automatically on next run with no forced reindex.

### Security

- Redact `Authorization: Basic` / `Proxy-Authorization` credentials and
  `Cookie:` / `Set-Cookie:` header values at ingest time, plus the
  `authorization` / `cookie` JSON keys. Pasted curl commands and HTTP dumps no
  longer leak these credentials into the searchable index or `clio show`.
- Enforce `0600` on the database `-wal`/`-shm` sidecars (not just the main DB file).

### Added

- Deleted source files are purged from the index. Removing a session file under
  `~/.claude/projects/` now removes it from search, reconciled by the MCP watcher
  backstop, CLI catch-up, and at MCP leader startup. Guards prevent a transient
  filesystem outage (or a renamed/moved file) from wiping live data.
- `clio doctor` now checks private-file permissions (DB + `-wal`/`-shm` are `0600`)
  and reports the count of source lines that could not be parsed.
- `ingest_state` tracks per-source unparseable-line counts (migration `0005`,
  applied automatically).

### Changed

- Incremental ingest streams complete lines with bounded memory and a per-line
  size cap instead of loading the whole unread tail, so a giant or corrupt line
  can no longer exhaust memory.
- Rewrite detection validates both a head and tail fingerprint; a same-size
  rewrite is now treated as a rewrite (full reingest) rather than a no-op.
- `context.Context` is threaded through the read data layer (search / sessions).
- `clio install-mcp` / `uninstall-mcp` edits to `~/.claude.json` are serialized
  across processes (Unix), so concurrent runs cannot lost-update each other.
- DB tuning: `cache_size` pragma and a post-migration `PRAGMA optimize`; search
  re-ranking widened for more accurate recency-aware ordering.

### Fixed

- Complete-but-unparseable lines are counted and surfaced via `clio doctor`
  instead of being silently dropped from the index.
- Ingest aborts a commit when the source file can no longer be validated
  (removed or replaced mid-ingest) instead of writing a stale snapshot.
- `pidAlive` treats `EPERM` (a live process owned by another user) as alive, so a
  valid MCP lease is not wrongly taken over.

## [0.2.0] - 2026-05-24

Safer search, stronger redaction, and a batch of correctness fixes. No breaking
changes.

### Added

- `clio show --json` (alias for `--format json`) and `--limit N`.
- Index on `sessions.ended_at`.

### Changed

- Operator-safe full-text search: FTS-special input (`c++`, `"unclosed`,
  `foo OR`, `(test`) matches as literal text instead of raising an FTS5 error.
- Hybrid query planner: 3+ character terms drive the FTS index while shorter
  terms (including 2-character CJK) filter the narrowed rows, avoiding a
  full-table scan when any single term is short.
- Structured, JSON-aware secret redaction replaces regex-on-serialized-text: a
  secret under a suspicious key is redacted as a whole subtree, and JSON embedded
  anywhere in a message is handled (fail-closed on pathological nesting depth).
- `activity_summary` day buckets use the local calendar day, not UTC.

### Fixed

- Session titles can no longer contain a raw secret (the first-user-message title
  is derived from already-redacted text); both `content` and `raw_json` covered.
- Correct `LIKE` escaping for `%`/`_` in content terms and `--project` prefixes.
- `clio doctor` exits non-zero when a check fails and no longer hides query errors
  behind a passing/zero-count result.
- Session-id resolution returns the exact match even when it is also a prefix of
  other ids.
- `clio show --format raw` no longer prints an event line once per expanded message.
- `install-mcp` refuses to overwrite a non-object `mcpServers` (and survives a
  `null` config) instead of clobbering it, and never leaves a stray `.bak`.
- `XDG_DATA_HOME` is honored only when absolute; faster UTF-8-safe truncation.

## [0.1.0] - 2026-05-23

Initial release.

### Added

- Index Claude Code session files (`~/.claude/projects/**/*.jsonl`) into a local
  SQLite + FTS5 database; local-first and read-only against the source files.
- CLI: `clio search`, `list`, `show`, `index`, `doctor`, `install-mcp`,
  `uninstall-mcp`.
- MCP server exposing `search`, `list_sessions`, `activity_summary`, and
  `read_session` so Claude Code can query its own history in-session.
- A file watcher that live-ingests new activity while the MCP server runs, with a
  periodic full-walk backstop; incremental catch-up on CLI commands otherwise.
- Secret redaction at ingest time (API keys, tokens, private keys, `.env`-style
  lines), in both the searchable text and the stored raw event.

[0.6.0]: https://github.com/linhn0617/clio/releases/tag/v0.6.0
[0.5.0]: https://github.com/linhn0617/clio/releases/tag/v0.5.0
[0.4.0]: https://github.com/linhn0617/clio/releases/tag/v0.4.0
[0.3.0]: https://github.com/linhn0617/clio/releases/tag/v0.3.0
[0.2.0]: https://github.com/linhn0617/clio/releases/tag/v0.2.0
[0.1.0]: https://github.com/linhn0617/clio/releases/tag/v0.1.0
