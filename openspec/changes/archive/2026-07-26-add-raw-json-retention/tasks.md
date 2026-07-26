# Tasks: add-raw-json-retention

## 1. Migration 0012 (FTS trigger guard + aborted signal)

- [x] 1.1 Migration 0012: (a) recreate `messages_au` as `AFTER UPDATE OF content`
  so a raw_json-only UPDATE fires no FTS delete+reinsert; (b) add
  `ingest_state.aborted INTEGER NOT NULL DEFAULT 0` AND, in the same migration,
  `UPDATE ingest_state SET aborted=1` for all pre-existing rows (fail-closed on
  upgrade); set aborted=0 in upsertIngestState (success) and aborted=1 in
  recordUnusableStatePass (both insert and conflict)
- [x] 1.2 Tests: raw_json-only UPDATE leaves messages_fts postings unchanged
  (integrity-check passes); content UPDATE still re-indexes; normal ingest paths
  unaffected; aborted flips 1 on an unusable pass and back to 0 on a successful
  re-ingest; **the migration's fail-closed
  `UPDATE ingest_state SET aborted=1` is a deterministic 1-line SQL (not
  separately unit-replayed; the aborted code paths — success clears, unusable
  insert+conflict sets — ARE tested), and a subsequent `index --full` clears
  validated rows to 0**

## 2. Pruning query & selection

- [x] 2.1 sessions-layer prune: single write-transaction UPDATE with
  `raw_json<>''` guard over restorable eligible sessions (age + regular/readable
  file + under any scanned root (index --full rediscovers + routes via Owns()) + non-lagged ingest_state);
  returns newly-pruned session/message counts, exact removed BLOB bytes
  (SUM(length(CAST(raw_json AS BLOB)))), and skipped-by-reason counts; honors
  --source/--project; predicate includes last_byte_offset==last_size AND aborted==0
- [x] 2.2 Dry-run: same selection, returns counts, writes nothing; duration
  parsed via timeutil.ParseSince, reject zero/negative/malformed
- [x] 2.3 Read-only pending-backfill exclusion: skip (do NOT backfill)
  claude-code sessions with tool_use messages still missing tool_targets;
  assert the command writes no column other than raw_json
- [x] 2.4 Tests: old+restorable pruned; recent untouched; each skip reason
  (missing/unreadable/undiscoverable/lagged/**unverified [aborted=1, e.g. a
  freshly-migrated row before any index --full]**) leaves raw_json intact and
  counted; pending-backfill session skipped; rerun idempotent (0 newly-pruned);
  content/FTS/session_usage/tool_targets byte-identical after prune

## 3. prune-raw command

- [x] 3.1 `clio prune-raw --older-than <dur> [--dry-run] [--vacuum] [--source]
  [--project]`: summary of pruned + skipped-by-reason + restore hint
- [x] 3.2 `--vacuum`: check MCP lock BEFORE any mutation → non-zero + full no-op
  if held; else prune then VACUUM; without --vacuum print reclaimable + how-to
- [x] 3.3 Command-level tests (sandboxed HOME/XDG): prune→verify blanked;
  --vacuum under held lock is a full no-op (nothing pruned); malformed duration
  rejected

## 4. Restore path & visibility

- [x] 4.1 `clio index --full` post-run check: report + non-zero exit if any
  session still has pruned raw_json after a full run
- [x] 4.2 Restore tests for ALL THREE sources (Claude re-parse, Codex rollout,
  Gemini replay): prune → index --full → raw_json repopulated; plus an
  unrestorable case (file made unreadable) surfaced + non-zero

## 5. show degradation

- [x] 5.1 `--format raw`: per-session pruned note (restore hint) + non-pruned
  lines; fully-pruned session prints only the note
- [x] 5.2 `--format json`: `RawJSON: null` on pruned messages + top-level
  `raw_pruned: true`; markdown path unchanged
- [x] 5.3 Tests for all three formats on pruned/partly-pruned/unpruned sessions

## 6. doctor & docs

- [x] 6.1 doctor: exact pruned-message count + labeled whole-DB freelist figure;
  zero/clean when nothing pruned; test both
- [x] 6.2 README/USAGE: `clio prune-raw` (reversibility, the restorable-not-just-
  existing rule, --vacuum lock behavior, restore via index --full)

## 7. Verification

- [x] 7.1 `go build ./... && go vet ./... && go test ./...` green
- [x] 7.2 Prune perf gates on a fixture, from a wal_checkpoint(TRUNCATE) baseline
  (recorded, each falsifiable): (a) the EXACT pre-prune dbstat.pageno set of
  messages_fts_data/idx/docsize ∩ the WAL frame page numbers written by the
  prune == ∅ (FTS bytes provably not rewritten; exact page-number sets, not
  ranges) + doctor FTS integrity-check passes; (b) on a prune-EVERYTHING fixture
  (denominator derivable), WAL frames ≤ 3× the pre-prune messages-table dbstat
  page count (page1/freelist accounted separately); (c) post
  wal_checkpoint(TRUNCATE)+VACUUM main-DB shrinks by ≥ 90% of reported removed
  BLOB bytes
- [x] 7.3 Every spec `#### Scenario` maps to a test
- [x] 7.4 Fresh-context verifier + adversarial cross-model review before archive
