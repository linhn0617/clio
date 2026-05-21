# clio Concurrent MCP — Design (Approach B: symmetric writers + single elected watcher)

- **Date:** 2026-05-21
- **Status:** Approved (brainstorming) + hardened after gstack eng review + codex adversarial review — pending implementation plan
- **Component:** `clio` (Go CLI exposing Claude Code history via an MCP server backed by SQLite)

## 1. Problem

When more than one Claude Code session is open, only the first session's `clio mcp`
child can start. Every later session's child exits immediately:

```
clio: another clio mcp server is already running; not starting a second writer
```

Claude Code reports this as MCP error `-32000` ("Connection closed"). The data,
binary, and index are all healthy — this is a **concurrency design limit**, not a
crash or stale process.

### Root cause (current behaviour, with refs)

- `internal/cli/mcp.go:45-50` acquires a **fatal exclusive lock** before serving;
  if held, it prints the message above and exits(1).
- `internal/lock/lock.go` implements that lock as a PID file: atomic
  `O_CREATE|O_EXCL` create, takeover only if the recorded PID is dead.
- The lock exists to protect SQLite from concurrent writers. But WAL mode and
  `busy_timeout(3000)` are already enabled (`internal/db/db.go:34`), all four MCP
  tools are read-only (`internal/mcp/tools.go`), and ingestion is **global** (it
  indexes every transcript under `~/.claude/projects/`).

So a single writer already indexes everyone's history. The exclusive lock is
stricter than necessary; the requirement is **safe multi-writer**, not
single-writer.

## 2. Goals / Non-goals

**Goals**
- Every concurrent session's `clio mcp` starts successfully and serves all tools.
- Every instance is a real **writer** (no instance is demoted to read-only).
- No SQLite corruption, no duplicate rows, no metadata drift, no DB-older-than-disk
  reversion, under concurrent ingestion.
- Indexing survives any single session dying (automatic failover, no split-brain).
- Stay within the existing architecture: `clio mcp` is a stdio subprocess of
  Claude Code. No separate daemon, no socket IPC, no network.

**Non-goals**
- No change to the MCP tool surface or output formats.
- No configurable lease/TTL knobs in v1 (hardcode sensible defaults).
- Not optimizing for very high concurrency (10+); target is 4–8 concurrent sessions.

## 3. Chosen approach (B)

Repurpose the lock from a **fatal exclusive gate** into a **renewable, fenced
leader lease**. Every instance opens the DB read-write and writes; only one elected
**leader** runs the continuous watcher, and leadership fails over automatically.

| Role | startup catch-up | continuous watcher | read-triggered catch-up | serves reads via | lease |
|---|---|---|---|---|---|
| **Leader** | yes | yes | not needed | RO connection | renews (fenced) |
| **Follower** | no (lazy) | no | yes, best-effort | RO connection | polls to promote |

This satisfies "every session writes" while avoiding N redundant watchers at 4–8
concurrency, with robust failover. The hardening below (§4.4–§4.7) is what makes
symmetric multi-writer actually safe — it is load-bearing, not optional.

## 4. Detailed design

### 4.1 Fenced heartbeat lease (`internal/lock`)

Replace the PID-only lock with a lease file (same path, `config.LockPath()`).

- **Contents:** `PID`, `generation nonce` (random 64-bit, fresh on each
  acquisition), and `last-renewed unix timestamp`.
- **TTL / cadence:** stale if `now - last_renewed > TTL`. Defaults: **TTL 10s,
  renew every 3s, follower promotion poll every 5s.** Worst-case idle failover gap
  ≈ `TTL + poll ≈ 15s` (documented honestly; see §5).
- **Liveness:** a follower may take over a stale lease when **either** the recorded
  PID is dead (signal-0 probe) **or** the heartbeat is older than TTL (covers a
  leader alive but hung).
- **Fencing (prevents split-brain):** the in-memory `Lease` remembers its own
  nonce. Every mutating op verifies ownership against the file first:
  - `Renew()` — re-read the file; if the nonce differs, **I have been
    superseded** → return `ErrSuperseded` (caller demotes, see §4.2). Only rewrite
    the timestamp if the nonce still matches.
  - `Release()` — re-read; only remove the file if the nonce matches mine. Never
    delete a successor's lease.
  - `TryPromote()` — atomic takeover writes a **new** nonce, so any returning old
    leader's `Renew()`/`Release()` become no-ops.
- **Atomic takeover:** write a temp file + `rename` (atomic replace), or
  remove-stale + `O_CREATE|O_EXCL`. Two simultaneous promoters: only one wins the
  atomic step; the loser re-reads, sees a fresh nonce, stays follower.
- **API:**
  - `AcquireOrFollow(path) (*Lease, isLeader bool, err error)` — never fatal.
  - `(*Lease) Renew() error` — returns `ErrSuperseded` if nonce no longer matches.
  - `(*Lease) TryPromote() (bool, error)` — true if it became leader (new nonce).
  - `(*Lease) Release() error` — no-op unless nonce matches.
- **Robustness:** tolerant parse of partial/truncated/old-format files → treated as
  stale (triggers takeover). PID-reuse is mitigated by the heartbeat: a reused PID
  that isn't renewing the lease still goes stale within TTL.

### 4.2 Roles, connections, and lifecycle (`internal/cli/mcp.go`)

**Read/write split (addresses read-path contention).** Every instance opens **two**
handles:
- an **RW** `*sql.DB` (`SetMaxOpenConns(1)`) used only for migrations, watcher
  ingest, and best-effort catch-up writes;
- an **RO** `*sql.DB` (`mode=ro`, WAL concurrent reads) used to serve all MCP read
  tools.

A read tool therefore **never** blocks on the write lock.

```
clio mcp starts
  → open RW handle → run migrations (fenced, §4.6)
  → open RO handle (serve reads)
  → lock.AcquireOrFollow(leasePath)
       ├─ isLeader → startup catch-up (IngestAll) → start watcher + renew loop
       └─ follower → no startup catch-up; start promotion poll (every 5s)
  → mcp.Serve(srv)   // read tools query the RO handle
```

- **Leader:** owns the watcher (`watcher.New(...).Run(ctx)`) + a renew goroutine
  calling `lease.Renew()` every 3s. On `ErrSuperseded` from Renew (a takeover
  happened while it was hung): **stop the watcher and renew loop, demote to
  follower** (start the promotion poll). This guarantees at most one live watcher.
- **Follower:** runs the promotion poll; on successful `TryPromote()` it runs a
  catch-up then starts the watcher + renew loop and stops polling.
- Only the leader does startup catch-up; followers stay fresh via §4.3 (avoids the
  N-way startup thundering herd when several sessions launch at once).

### 4.3 Follower freshness (best-effort read-triggered catch-up)

Before serving a read tool, a follower runs an **incremental catch-up** so results
are fresh at query time. Hardened:
- The catch-up writes via the **RW** handle; reads are served from the **RO**
  handle. They do not share a connection.
- The catch-up is **best-effort**: if it cannot get the write lock within
  `busy_timeout`, it logs and proceeds to serve the read against the RO handle
  (data is at most ~500ms stale, since the leader's watcher is indexing globally).
  A read is **never** failed or blocked because ingestion is busy.
- Reuses the existing watermark logic (`ingest_state`); unchanged files skip
  cheaply.

### 4.4 Cross-process write correctness

Applies to all writers (leader watcher + follower catch-up).

1. **IMMEDIATE transactions.** Add `_txlock=immediate` to the RW DSN
   (`internal/db/db.go:34`). Confirmed supported by `modernc.org/sqlite@v1.50.1`
   (`driver.go:87`, `sqlite.go:195`). This takes the write lock at `BEGIN` and
   serializes writers; `busy_timeout(3000)` retries on contention. **It does not by
   itself prevent the stale-snapshot races below** — the file read and change
   classification happen *before* `Begin` (`ingest.go:945-1027`,
   `incremental.go:33`). Serialization + the guards in 2–4 together are what make
   it safe.

2. **Idempotent message rows.** New migration `0003` (see §4.6):
   - `CREATE UNIQUE INDEX ON messages(session_uuid, seq)`.
   - `ingest.go:1094` insert → `INSERT OR IGNORE`. After insert, branch on
     `RowsAffected()`: `1` → use `LastInsertId()` for dependent `tool_calls`;
     `0` (duplicate ignored) → the message and its tool_calls already exist, **skip**
     the tool_calls inserts.

3. **In-transaction file re-validation (prevents DB-older-than-disk reversion).**
   The bug: writer A reads file snapshot V1, the file is atomically replaced by V2,
   writer B commits V2, then A gets the write lock and `changeFull`
   DELETE+reinserts the stale V1 — DB ends up older than disk. Fix: capture the
   file `(size, mtime)` at read time; inside the IMMEDIATE transaction, **re-stat
   the file and re-load `ingest_state`**; if either changed since the snapshot,
   **rollback and signal "changed under us"** so the next watcher tick / catch-up
   re-ingests the fresh bytes. (Residual same-second, same-size rewrite is caught
   by the tail fingerprint on the next pass + the 60s backstop.)

4. **Monotonic watermark guard.** The `ingest_state` upsert (`ingest.go:1119`) only
   advances: `... ON CONFLICT(source_file) DO UPDATE SET ... WHERE
   excluded.last_byte_offset >= ingest_state.last_byte_offset`. A stale writer can
   never move the offset backward.

5. **Idempotent `turn_count` (prevents metadata drift).** The bug: incremental
   ingest does `turn_count = turn_count + userTurns` (`ingest.go:1133` upsert),
   so two writers parsing the same appended message each increment it — rows
   dedupe but `sessions.turn_count` drifts upward and never self-heals. Fix:
   inside the same transaction, after inserting messages, set
   `turn_count = (SELECT COUNT(*) FROM messages WHERE session_uuid = ? AND
   role = 'user')`. Authoritative and concurrency-immune; replaces both the
   `+=` (incremental) and the parsed-`userTurns` (full) paths.

6. **`SetMaxOpenConns(1)` retained** on the RW handle (serializes intra-process
   writes); cross-process serialization is SQLite's write lock + busy_timeout.

### 4.5 Why this is corruption-free (corrected argument)

- **Duplicate messages:** prevented by `UNIQUE(session_uuid, seq)` + `INSERT OR
  IGNORE` (4.4.2), not by IMMEDIATE alone.
- **Stale-snapshot reversion / lost full re-ingest:** prevented by in-transaction
  re-validation (4.4.3) — a writer that read pre-replacement bytes aborts instead
  of committing them. The earlier claim that "full re-ingest final state is
  identical" was **wrong** and is replaced by this guard.
- **Backward watermark / redundant re-scan:** prevented by the monotonic guard
  (4.4.4).
- **turn_count drift:** prevented by recomputing from `COUNT(*)` (4.4.5).
- **FTS5:** external-content with triggers (`0001_init.sql:31-49`). `INSERT OR
  IGNORE` that is ignored does not fire `messages_ai`; the migration dedup DELETE
  fires `messages_ad`. FTS stays in sync (confirmed least-broken by both reviews).

### 4.6 Concurrent-safe migrations (`internal/db/db.go`)

The bug: every process runs `migrate()` on startup with no cross-process guard
(`db.go:764`); on first run after shipping `0003`, two processes both see it
absent, both run it, one wins `INSERT INTO schema_migrations`, the other fails the
PK and exits — violating "every session starts." Fix:
- Run each pending migration inside a **`BEGIN IMMEDIATE`** transaction.
- **Re-check** `schema_migrations` for that name *inside* the transaction; if now
  present, skip (another process applied it).
- Record with `INSERT OR IGNORE INTO schema_migrations`.
- SQLite DDL (`CREATE INDEX`, the dedup `DELETE`) is transactional, so the apply +
  marker commit atomically; the losing process sees the marker and skips cleanly.

### 4.7 Migration 0003 contents

- `CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_uuid, seq);`
  preceded by a safe dedup on existing DBs:
  - `DELETE FROM messages WHERE id NOT IN (SELECT MIN(id) FROM messages GROUP BY session_uuid, seq);`
    (fires `messages_ad` → FTS stays in sync).
  - **Then delete orphaned tool_calls** (no FK/cascade exists,
    `0001_init.sql:51`): `DELETE FROM tool_calls WHERE message_id NOT IN (SELECT id FROM messages);`
  - Order: dedup messages → delete orphan tool_calls → create unique index.

## 5. Error handling / edge cases

- **Write lock busy >3s:** catch-up is best-effort (serves stale, §4.3); watcher
  ingest logs and retries next tick. No user-visible failure.
- **Two simultaneous promotions:** atomic takeover + nonce; loser stays follower.
- **Leader hung past TTL:** follower promotes (new nonce); the hung leader, on
  resuming, gets `ErrSuperseded` from Renew → demotes, stops its watcher. No
  split-brain, no lease theft.
- **Clock skew:** backward step can delay failover; forward step can cause early
  promotion. Acceptable for a local single-user tool; documented. (Monotonic OS
  clocks are not assumed across processes.)
- **PID reuse:** mitigated by heartbeat — a reused PID not renewing goes stale
  within TTL.
- **File replaced mid-ingest:** in-txn re-validation aborts the stale commit (§4.4.3).
- **Concurrent first-run of 0003:** resolved by fenced migration (§4.6).

## 6. Backward compatibility

- `0003` is additive + self-healing (dedup before index; orphan cleanup).
- `_txlock=immediate` and the RO serving handle are transparent.
- Lease file gains a nonce; old-format files parse as stale (safe takeover).
- No change to MCP tool I/O.

## 7. Testing strategy

- **Cross-process concurrency test (key, must be real OS processes).** Goroutines
  on one `*sql.DB` collapse onto a single connection and exercise neither
  cross-process SQLite locking nor lease races — the existing `ingest_test.go:188`
  "concurrent read" test proves almost nothing. The new test spawns **N
  subprocesses** (exec a tiny test harness / the clio binary) ingesting the same
  continuously-growing file, plus forced **rewrite/truncation** mid-run. Assert:
  - no duplicate `(session_uuid, seq)` rows;
  - `messages_fts` row count == `messages` row count;
  - `sessions.turn_count` == actual user-message count (no drift);
  - DB content is never older than the on-disk file at quiescence (no reversion);
  - watermark is monotonic;
  - no errors / no exits.
- **Lease unit tests (injectable clock + PID probe):** acquire / renew / stale-by-PID
  / stale-by-heartbeat / two-promoter race / **fencing** (superseded leader's Renew
  returns ErrSuperseded and Release is a no-op).
- **Fenced migration test:** two processes apply `0003` concurrently; both end up
  running, schema correct, no PK failure.
- **Failover test:** kill leader; a follower promotes within `TTL + poll`; newly
  appended lines get indexed; demoted ex-leader does not double-watch.
- **Read-path test:** read tools succeed (RO handle) even while a writer holds the
  write lock; follower returns rows for content written after startup.
- All existing tests must still pass.

## 8. Scope / YAGNI

- No daemon, socket IPC, or network.
- Intervals hardcoded (TTL 10s, renew 3s, promotion poll 5s).
- Tool surface and output formats unchanged.

### NOT in scope
- WAL checkpoint policy / size guard / observability — both reviews ranked it
  low (no long-lived explicit read transaction exists today). Add a note in code;
  revisit only if the `-wal` file is observed growing. Deferred.
- Configurable TTLs — YAGNI for a single-user local tool.
- Removing `SetMaxOpenConns(1)` — keep; the RO handle already gives read concurrency.

### What already exists (reused, not rebuilt)
- `ingest_state` watermark + tail fingerprint (`incremental.go`) — reused by
  read-triggered catch-up and the re-validation guard.
- `openForQuery()` defer-to-writer pattern (`common.go:425`) — generalized into the
  follower read path.
- `lock` package atomic-create pattern — extended into the fenced lease.
- `watcher` — unchanged behaviour, just leader-gated.

## 9. File-by-file change map

- `internal/lock/lock.go` — fenced lease: nonce + heartbeat, `AcquireOrFollow`,
  `Renew` (ErrSuperseded), `TryPromote`, `Release` (nonce-checked), tolerant parse.
- `internal/cli/mcp.go` — two handles (RW + RO); fenced migration call; leader vs
  follower wiring; renew loop with demote-on-supersede; promotion poll.
- `internal/db/db.go` — `_txlock=immediate` on RW DSN; RO serving handle plumbing;
  `migrate()` per-migration IMMEDIATE txn + in-txn re-check + `INSERT OR IGNORE`.
- `internal/db/migrations/0003_messages_unique.sql` — dedup messages → delete
  orphan tool_calls → unique index.
- `internal/ingest/ingest.go` — `INSERT OR IGNORE` + `RowsAffected` for tool_calls;
  in-txn file re-validation (rollback on change); monotonic watermark guard;
  `turn_count` recomputed via `COUNT(*)`.
- `internal/mcp/tools.go` (or shared helper) — read tools query the RO handle;
  follower best-effort catch-up via the RW handle before serving.
- `internal/cli/common.go` — align `IsHeld()`/`openForQuery()` with the new lease
  (a live leader still reads as "held").
- Tests as in §7 (new subprocess harness; rework `ingest_test.go:188`).
