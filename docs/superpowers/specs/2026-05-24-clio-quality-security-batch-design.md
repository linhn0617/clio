# clio quality & security batch — design

Date: 2026-05-24
Status: design approved, pending OpenSpec proposals

## Context

A full-codebase review combined three inputs:

1. Three Claude `Explore` agents (per-package sweep). Produced many findings, but
   verification against the source showed most "CRITICAL/HIGH" items were false
   positives (e.g. `.bak` "data loss" — impossible under atomic rename; watcher
   debounce "goroutine leak" — guarded by `debounce == nil`; `synchronous(NORMAL)`
   "unsafe" — correct for WAL; "missing `sessions(uuid)` index" — it is the PRIMARY
   KEY; "add FTS adversarial tests" — already present).
2. Manual verification of every high-severity claim against the code.
3. A `codex` adversarial review of the actual committed code. Codex confirmed all
   three "non-bugs" above were correctly judged, found no FTS5/LIKE injection
   breakout, and confirmed migration idempotency — but surfaced two real issues the
   Claude agents missed: a secret-redaction gap and a silent ingest data-loss path.

The codebase is high quality (clean `build`/`vet`/`test -race`, good test ratio,
careful concurrency). This batch fixes the genuine findings only; it does not
refactor working code.

## Findings in scope (verified)

| # | Finding | Source | Capability |
|---|---------|--------|------------|
| C5 | Redaction misses `Authorization: Basic`, `Cookie`/`Set-Cookie` headers, and JSON keys `authorization`/`cookie` | codex | session-ingest |
| C3 | Watermark advances past a complete-but-unparseable line → silent permanent loss until full reindex | codex | session-ingest |
| C1 | Stale-snapshot re-validation in `commit()` ignores `os.Stat` errors and compares only size+mtime | codex | session-ingest |
| C4 | `readFrom` uses `io.ReadAll` on the whole unread tail → OOM / repeated re-read on a huge no-newline tail | codex | session-ingest |
| C2 | 512-byte tail fingerprint can miss a pre-watermark rewrite | codex | session-ingest |
| C7 | Watcher ignores Remove/Rename and backstop never purges → deleted sessions stay searchable forever | codex | file-watcher |
| C8 | `pidAlive` treats `EPERM` as dead (and is vulnerable to PID reuse) | codex | lock (internal) |
| C9 | `-wal`/`-shm` sidecars don't get 0600; exposed if the data dir pre-exists with loose perms | codex | db (internal) |
| C6 | `claudeconfig.mutate` is an unlocked cross-process read-modify-write → lost update | codex + review | mcp-installer |
| R1 | Data layer threads no `context.Context`; MCP handlers discard `ctx` → no cancellation/timeout | review | mcp-server + data layer |
| R4 | DB DSN sets no `cache_size`; no post-migration `ANALYZE` | review | db (internal) |
| R3 | Search re-rank `overscan=5` can drop a recency-boosted hit ranked just outside the bm25 top-N | review | fts-search |
| R2 | Initial full index does per-row `tx.Exec` (no prepared statements / batching) | review | db (internal) |
| R6 | Nits: unchecked `json.Marshal` in `AddServer`; `doctor` doesn't check file perms; `group_by` validated only at query time | review | mcp-installer / diagnostics / mcp-server |

Explicitly out of scope (judged not real): `.bak` redundancy, debounce leak,
`synchronous(NORMAL)`, FTS injection, `sessions(uuid)` index, `pingMaxAttempts`
vs `busy_timeout`. See Context for why.

## Resolved design decisions

1. **Source-file deletion (C7): confirm-then-purge.** Remove/Rename events do NOT
   trigger an immediate delete (fsnotify fires Remove/Rename during atomic
   temp→rename writes, so a raw event is not authoritative). The 60s backstop is the
   authority: it reconciles `ingest_state` / `sessions.source_file` against the
   filesystem and purges rows only for sources confirmed gone. This matches the
   user mental model "I deleted the conversation, clio forgets it too."

2. **Unparseable line (C3): advance, count, surface.** Keep advancing the watermark
   (one permanently-bad line must never wedge a file's ingest forever), but record a
   persistent skipped-line counter and report it via `doctor` ("N lines could not be
   parsed; after upgrading clio, run `clio index --full`"). This preserves liveness
   and removes the *silent* part of the loss. (Rejected: "stop and retry" — wedges
   the file on a permanent bad line; "byte-range quarantine" — adds schema and
   bookkeeping beyond this batch's scope.)

## Change set (4 OpenSpec changes, implemented in order)

### ① `harden-redaction-followup` — session-ingest (security, first)

- Add redaction rules: `Authorization: Basic <base64>`, `Cookie:` / `Set-Cookie:`
  header values. Add `authorization`, `cookie`, `set-cookie` to `isSecretKey`.
- Patterns stay conservative (anchored on header shape / `Basic ` + base64) to
  protect searchability — false positives are the main risk.
- Spec delta: MODIFY "Secret redaction during ingest" — add scenarios for Basic
  auth header, cookie header value, and `authorization`/`cookie` JSON key.
- Tests (TDD): one `redact_test.go` case per pattern; existing "no raw secret in
  title/content/raw_json" regression still passes.

### ② `fix-incremental-ingest` — session-ingest (data correctness)

The "ingest state machine" change deferred from `polish-review-p3`.

- C3: on a complete-but-unparseable line, skip it, increment a persistent
  skipped-line counter, and keep advancing the watermark (see decision 2).
- C1: abort `commit()` re-validation on any `os.Stat` error; strengthen the
  same-file check beyond size+mtime.
- C4: replace `io.ReadAll` with a bounded read that resumes on the next pass
  (persist partial-line state).
- C2: detect a pre-watermark rewrite and fall back to full reingest.
- Spec delta: session-ingest incremental-resume + bad-line scenarios; diagnostics
  "doctor reports unparseable-line count".
- Eng-review risk to resolve: bounded-read behavior when a single line exceeds the
  read cap (no newline in the window) — log+advance vs grow cap.

### ③ `fix-watcher-deletions` — file-watcher + lock (correctness/privacy)

- C7: handle Remove/Rename; backstop reconciles and purges confirmed-gone sources
  (decision 1).
- C8: `pidAlive` treats `EPERM` as alive; test via a signal seam.
- Spec delta: file-watcher — ADD "backstop reconciles and purges deleted sources".
  `pidAlive` is an internal fix (no spec capability).

### ④ `polish-review-p4` — cross-cutting (polish + hardening)

- R1: thread `context.Context` through the data layer (`QueryContext`/`ExecContext`)
  and pass `ctx` from MCP handlers and CLI. No behavior change; widest diff.
- R4: add `cache_size` pragma; run `ANALYZE` once after migrations.
- C9: enforce 0600 on `-wal`/`-shm`; verify the data dir is 0700.
- R3: tune search `overscan`.
- C6: serialize `claudeconfig.mutate` across processes (reuse `internal/lock` or a
  lockfile).
- R6: `doctor` checks db/config/lock file perms; validate `group_by` at the MCP
  boundary; check the `json.Marshal` error in `AddServer`.
- R2 (measure first): add a benchmark for the initial full index. Implement
  prepared statements / batched inserts ONLY if the benchmark shows a meaningful
  gain on a realistic history; otherwise record "measured, not worth it" and drop
  (repo culture: no unrequested optimization).
- Spec delta: mcp-installer (claudeconfig concurrency + json safety), diagnostics
  (doctor perm checks), mcp-server (group_by validation). Internal-only items carry
  no spec delta, matching the `polish-review-p3` precedent.

## Process

- Each change is a separate OpenSpec change (`openspec/changes/2026-05-24-<name>/`
  with `proposal.md`, `design.md`, `tasks.md`, `specs/<capability>/spec.md`).
- TDD per task (failing test first), matching existing `tasks.md` style.
- After each change is implemented: self code-review + `codex` adversarial review
  before starting the next change.
- Planning (OpenSpec proposals) happens in plan mode; the plan is reviewed with
  gstack before implementation begins.

## Eng-review outcome (2026-05-24)

`/plan-eng-review` + codex outside-voice ran against the 4 OpenSpec changes. 10 findings,
all resolved into the plan; 0 unresolved, 0 critical gaps remaining.

Decisions folded in:

- D1 (③): `PurgeMissing` gets a root guard (`os.ReadDir`, not `os.Stat`) and a safety cap
  that suppresses only when the missing set is BOTH a large absolute count AND >50% of
  sources — so small installs still purge, but a filesystem outage never wipes the index.
- D2 (②): empty `head_fingerprint` (pre-0005 rows) is treated as unknown — skip the head
  check and backfill, so upgrades do not force a full reindex.
- D3 + outside-voice #1 (②): single-line cap `maxLineBytes` (16 MB); an over-cap line is
  discarded+counted ONLY after its terminating newline is observed; EOF-before-newline
  leaves the watermark (in-progress append), never advances to EOF.
- Outside-voice #2 (②): `unparsed_lines` ACCUMULATES across incremental passes, resets only
  on full reingest — per-run overwrite would let `doctor` go green while old loss persists.
- Outside-voice #4 (④): context threading is hygiene/groundwork only; the "enables
  cancellation/timeout" claim is dropped because `beforeRead()` catch-up runs synchronously
  before queries. Background ingest uses a long-lived ctx, never a request ctx.
- Outside-voice #5 (④): claudeconfig serialization is scoped to Unix in the spec
  (`mutex_other.go` is a no-op); Windows relies on atomic rename (no corruption).

## NOT in scope (deferred)

- Bounding/parallelizing the follower `beforeRead()` catch-up so cancelled MCP requests
  return promptly (changes the request path; its own change).
- Real Windows file locking (`LockFileEx`) for claudeconfig.
- Whole-prefix hashing for rewrite detection (append-only files don't need it).
- Chunked multi-commit ingest (single-transaction-per-file is kept).
- Defeating PID reuse in `pidAlive`.
- prepared-statement inserts unless the ④ benchmark proves a win (measure-first).

## Parallelization

Changes ①, ②, ③ all touch `internal/ingest` (and ② adds migration 0005 that ③'s purge
SQL and ④'s context threading sit near), so they are best done sequentially on one branch
in order ① → ② → ③ → ④, matching the security → data → watcher → polish priority. Within
④, the items are independent but share files with the earlier changes, so ④ goes last.
No safe parallel worktree split; sequential implementation.

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 0 | — | — |
| Codex Review | `/codex` | Independent 2nd opinion | 2 | issues_found | code: redaction+watermark found; plan: 6 findings, all folded |
| Eng Review | `/plan-eng-review` | Architecture & tests (required) | 1 | CLEAR | 10 issues, all resolved; 0 critical gaps |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | — | n/a (no UI) |
| DX Review | `/plan-devex-review` | Developer experience gaps | 0 | — | — |

- **CODEX:** code-stage adversarial review found the redaction + watermark bugs; plan-stage outside voice found 6 issues (over-cap EOF, unparsed_lines semantics, purge cap, cancellation overclaim, Windows lock, root-guard stat) — all folded into the plan.
- **CROSS-MODEL:** Claude eng-review and codex agreed the incremental-ingest state machine and purge heuristic were the weak spots; both confirmed redaction (①) is sound.
- **UNRESOLVED:** none.
- **VERDICT:** ENG CLEARED — ready to implement, sequential ① → ② → ③ → ④.
