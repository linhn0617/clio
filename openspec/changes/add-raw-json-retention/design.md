# Design: add-raw-json-retention

Revised after round-1 adversarial review (GO-WITH-CHANGES; 2 P1 + 6 P2/P3, all
folded in — marked `[R1-n]`).

## Context

Measured on a real 57,514-message clio DB (2,244 MB, ~4.4× source after
VACUUM): `messages_fts_data` (trigram index) is 1,517 MB and structural;
`messages.raw_json` is 221 MB and, unlike the FTS index, is a redaction-
processed copy of a line still present in the source `.jsonl`. FTS5 is
external-content over `messages.content` only (0001_init.sql: `content =
'messages'`), so `raw_json` feeds neither search nor `session_usage`. Its only
readers are `clio show --format raw|json` (show.go writeRaw) and the activity
backfill that reparses it into `tool_targets`. `raw_json` is `TEXT NOT NULL`;
`sessions.source_file` records each session's origin file.

**Critical prior-art the review surfaced:** (a) the `messages_au` trigger
(0001_init.sql:46) fires on *every* UPDATE to `messages`, doing a full FTS
delete+reinsert of `content` — so a naive `UPDATE ... SET raw_json=''` rewrites
all FTS postings and churns the WAL, which would violate the byte-identical
claim and could defeat the space goal. (b) Activity backfill
(activity.go:60, common.go:109) is **not** one-time — it runs during ordinary
catch-up whenever a claude-code tool_use message still lacks `tool_targets`, and
reparses `raw_json`. (c) `os.Stat(source_file) == exists` does **not** prove
restorability.

## Goals / Non-Goals

**Goals:**
- Reclaim the ~10% of the DB that `raw_json` occupies for old sessions, via an
  explicit manual command, without churning the FTS index (WAL necessarily
  records the touched `messages` pages — see the D1 gates).
- Make pruning non-destructive by construction: prune only sessions whose raw
  form is *demonstrably restorable*, and surface restore failures.
- Leave `content`, FTS postings, `session_usage`, quota, and extracted
  `tool_targets` byte-identical.

**Non-Goals:**
- Automatic/scheduled pruning or any config-file retention setting.
- Pruning `content`/FTS, or reconstructing raw form during `show`.

## Decisions

**D1 — FTS trigger must be guarded; this REQUIRES a migration. [R1-P1-FTS]**
The prior "no migration" decision is retracted. New migration `0012` (a) recreates
`messages_au` as `AFTER UPDATE OF content` (SQLite column-scoped update trigger)
so an update touching only `raw_json` does not fire an FTS delete+reinsert, and
(b) adds `ingest_state.aborted INTEGER NOT NULL DEFAULT 0` and then, in the same
migration, `UPDATE ingest_state SET aborted = 1` for all pre-existing rows —
an explicit successful-vs-aborted-ingest signal, **fail-closed on upgrade**:
`DEFAULT 0` alone would mark every historical row (including old aborted-rewrite
rows) as successful, so existing rows are initialized aborted/unverified and
only a subsequent successful commit (`index --full`) clears them to 0. See D3.
Normal ingest never UPDATEs `content` (it INSERT-OR-IGNOREs new messages and
DELETE+INSERTs on full rebuild — verified), so the scope narrowing is
behavior-preserving for every existing path while making raw-only pruning
FTS-neutral. **Gates (separated, since blanking B-tree pages MUST write WAL —
only FTS churn can be ~zero, not total WAL) [R2-P2 / R3-P2], measured on a
representative fixture, each falsifiable**:
Baseline: start from a `wal_checkpoint(TRUNCATE)`ed DB (empty WAL), run one
prune, inspect the resulting WAL before the next checkpoint.
(a) **FTS provably untouched (byte-level)**: the exact set of `dbstat.pageno`
values belonging to `messages_fts_data`/`messages_fts_idx`/`messages_fts_docsize`
(taken pre-prune) has EMPTY intersection with the set of page numbers written to
the WAL by the prune. Zero-intersection proves those FTS pages' bytes were not
rewritten — stronger than an unchanged page COUNT (which delete+reinsert can
preserve). Uses exact per-page `dbstat.pageno` sets, not min/max ranges.
`clio doctor`'s FTS integrity-check also passes.
(b) **WAL bounded to the messages heap**: measured on a **prune-everything**
fixture so the denominator is derivable — the pruned rows are ALL messages, so
the "touched pages" set is exactly the pre-prune `dbstat.pageno` set of the
`messages` table. WAL frames written ≤ 3× that page count (a small constant for
interior/overflow updates; page 1 / freelist pages accounted separately).
Combined with (a), this shows the prune's writes are confined to the `messages`
heap.
(c) **Shrink real**: after `wal_checkpoint(TRUNCATE)` + `VACUUM`, main-DB size
(`page_count × page_size`) drops by ≥ 90% of the reported removed BLOB bytes.

**D2 — Manual command, transactionally revalidated selection. [R1-P2-conc]**
`clio prune-raw --older-than <dur> [--dry-run] [--vacuum] [--source <name>]
[--project <prefix>]`. `--older-than` reuses the existing since-grammar
(`internal/timeutil.ParseSince`, which accepts `14d`/`12h`/`YYYY-MM-DD`; Go's
`time.ParseDuration` rejects `d`, so it is NOT used) and MUST resolve to a past
cutoff (reject zero/negative/malformed). Eligibility is a single SQL statement
run *inside the write transaction* so it cannot race an interleaved ingest:
`UPDATE messages SET raw_json='' WHERE raw_json<>'' AND session_uuid IN
(<restorable eligible sessions>)`. The `raw_json<>''` guard makes reruns
idempotent and makes reported counts = newly-pruned rows. The filesystem
existence check (D3) is an unavoidable TOCTOU against deletion between check and
commit; documented, and bounded by D3's restorability requirement.

**D3 — "Restorable", not merely "exists". [R1-P1-restore]**
A session is prunable iff ALL hold: its most recent activity is strictly older
than the cutoff; its `source_file` is a **regular, readable** file (not just
`Stat`-able); it lies **under any currently-scanned source root** (so `index --full` rediscovers
it and routes it to the owning adapter via Owns()); and its `ingest_state`
snapshot is a **completed, current** ingest of the file — ALL of
`last_size == fi.Size()`, `last_mtime == fi.ModTime()`, `last_byte_offset ==
last_size` (fully ingested to EOF), AND `aborted == 0`. The `aborted` flag is
load-bearing and offset alone is insufficient: `recordUnusableStatePass` records
the current size+mtime and, **on conflict, preserves the prior offset** — so a
previously-successful file (size=offset=100) that is rewritten same-size and
then fails to parse would satisfy size+mtime+offset yet was never committed.
Migration 0012 adds `aborted`, initializes all pre-existing rows to `1`
(fail-closed — see D1), and the successful commit path (`upsertIngestState`)
sets `aborted = 0` while `recordUnusableStatePass` sets `aborted = 1`, making
successful completion independently observable. **Upgrade precondition
(documented):** after installing this version, a session is prunable only once a
successful `clio index --full` has re-validated it (clearing `aborted` to 0) —
which is the same full reindex users already run to backfill, so it composes;
until then `prune-raw` skips the session, reported under the "unverified" skip
reason. **Trust boundary (honest):** this is not a cryptographic identity — a
pathological same-size *and* same-mtime in-place content swap would escape it.
But that exact case ALREADY corrupts clio independent of pruning: `classifyChange`
returns `changeSkip` for same-size+same-mtime, so clio's incremental ingest
already trusts this pair and would already mis-serve such a swapped file. Pruning
therefore introduces **no new trust assumption** — its restorability envelope
equals clio's existing indexing envelope. For every failure clio itself can
detect (file gone/unreadable/undiscoverable, size/mtime/offset mismatch), the
D3 predicate skips it and the `index --full` post-run check (below) exits
non-zero on any residual unrestored prune. The one uncovered edge is exactly the
one clio already cannot detect and already breaks on; we do not add a per-session
full-file digest to defend a case no source adapter produces (append-only
session files grow — they are not rewritten in place at identical size+mtime). Sessions failing any of these are **skipped and counted by reason**
(missing/unreadable/undiscoverable/lagged), never pruned. Restoration is the
existing `clio index --full` force path (re-parse → re-store `raw_json`; Gemini
via whole-file replay, Codex via rollout, Claude via full re-parse). Because
`IngestAll` logs per-file failures and continues, **`clio index --full` gains a
post-run check**: it reports (and exits non-zero on) any session that still has
pruned `raw_json` after a full run, so a silent unrestored prune is impossible.

**D4 — Activity backfill must not be stranded — via READ-ONLY skip, not a
write. [R1-P2-backfill / R2-P1]**
`prune-raw` MUST NOT run `BackfillActivity` (it INSERTs `tool_targets`, which
would break both `--dry-run` "no writes" and the "tool_targets byte-identical"
guarantee). Instead eligibility **excludes, read-only**, any claude-code
session that still has tool_use messages without `tool_targets` (a pending
backfill that would need the `raw_json`). Those sessions are skipped and
counted; the user runs normal ingest/`index` (which backfills) first, then
re-prunes. This keeps `prune-raw` a pure raw_json blanking operation that
touches nothing else.

**D5 — Pruned sentinel + show/JSON degradation.**
`raw_json` stays NOT NULL; `''` is the pruned marker (every current parser emits
messages only from non-empty JSON event lines, and redaction cannot yield empty
— documented as a required source-adapter invariant so future adapters keep it).
`show --format raw`: per-session note (`raw form pruned — restore with 'clio
index --full'`) plus any non-pruned lines. `--format json`: pruned messages
carry `"raw_json": null` and the payload carries `"raw_pruned": true`.
`--format markdown` unchanged.

**D6 — `--vacuum` lock-checked BEFORE any mutation; refusal is a no-op; output
metric defined; dry-run precedence. [R1-P2-vacuum / R2-P2]**
When `--vacuum` is requested the command checks the MCP index lock *before*
pruning; if held it exits non-zero with **no** database change (no prune, no
vacuum). With the lock free it prunes then VACUUMs. **Prune output metric**: the
command reports the **exact logical raw bytes it removed** —
`SUM(length(CAST(raw_json AS BLOB)))` (BLOB length = true UTF-8 bytes; plain
`length()` counts characters, undercounting the Chinese-heavy corpus) computed
over the target rows *before* blanking, `COALESCE`-guarded (a real, reproducible
number, distinct from doctor's whole-DB freelist figure) — plus,
without `--vacuum`, the "run VACUUM to reclaim on disk" hint. **`--dry-run`
takes precedence over `--vacuum`**: `--dry-run [--vacuum]` performs no lock
check, no prune, and no VACUUM — it only reports the would-remove bytes and
skipped-by-reason counts.

**D7 — Doctor reporting is precisely defined. [R1-P2-estimate]**
Original `raw_json` lengths are gone after pruning, so doctor reports two
well-defined numbers: the **exact pruned-message count**
(`COUNT(*) WHERE raw_json=''`), and the **global freelist size**
(`freelist_count × page_size`) labeled as "reclaimable on VACUUM (whole-DB
freelist, not raw-only)". No fabricated per-raw byte estimate.

## Risks / Trade-offs

- [Prune churns FTS] → D1 trigger guard makes FTS page delta exactly 0
  (falsifiable gate a). WAL necessarily records the touched `messages` pages;
  gate (b) bounds it and proves the FTS index is untouched.
- [`index --full` silently fails to restore] → D3 post-run pruned-residual
  check makes it non-zero/visible.
- [Selection races an ingest] → D2 single-transaction revalidation; residual
  filesystem TOCTOU documented and bounded by D3.
- [Stranded backfill] → D4 does NOT run backfill; it read-only excludes
  claude-code sessions with pending `tool_targets`, so nothing is stranded and
  the command writes only `raw_json`.
- [Future adapter emits empty raw_json] → D5 documents the non-empty invariant.

## Migration Plan

Migration `0012` recreates `messages_au` scoped to `content` AND adds
`ingest_state.aborted` (existing rows set to 1, fail-closed). Additive/behavior-
preserving for existing paths; the fail-closed init means prune-raw skips all
sessions until a validating `index --full` runs (the same reindex users already
run to backfill). Rollback: the old trigger form is a strict
superset of firings; restoring it (or never pruning) is safe, and `index --full`
restores any already-pruned `raw_json`.

## Open Questions

- `--keep-recent <N>` mode — deferred; `--older-than` covers the stated need.
