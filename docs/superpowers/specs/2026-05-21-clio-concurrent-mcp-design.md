# clio Concurrent MCP — Design (Approach B: symmetric writers + single elected watcher)

- **Date:** 2026-05-21
- **Status:** Approved (brainstorming) — pending implementation plan
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
- The lock exists to protect SQLite from concurrent writers. But:
  - WAL mode and `busy_timeout(3000)` are **already enabled** (`internal/db/db.go:34`).
  - All four MCP tools (`search`, `list_sessions`, `read_session`,
    `activity_summary`) are **read-only** (`internal/mcp/tools.go`).
  - Writes happen only via startup catch-up + the background `fsnotify` watcher,
    and ingestion is **global** (it indexes every transcript under
    `~/.claude/projects/`, not just the current session).

So a single writer already indexes everyone's history. The exclusive lock is
stricter than necessary; the real requirement is **safe multi-writer**, not
single-writer.

## 2. Goals / Non-goals

**Goals**
- Every concurrent session's `clio mcp` starts successfully and serves all tools.
- Every instance is a real **writer** (no instance is demoted to read-only).
- No SQLite corruption, no duplicate rows, under concurrent ingestion.
- Indexing survives any single session dying (automatic failover).
- Stay within the existing architecture: `clio mcp` is a stdio subprocess of
  Claude Code. No separate daemon, no socket IPC, no network.

**Non-goals**
- No change to the MCP tool surface or output formats.
- No configurable lease/TTL knobs in v1 (hardcode sensible defaults).
- Not optimizing for very high concurrency (10+); target is the user's typical
  4–8 concurrent sessions.

## 3. Chosen approach (B)

Repurpose the lock from a **fatal exclusive gate** into a **renewable leader
lease**. Every instance opens the DB read-write and writes; only one elected
**leader** runs the continuous watcher, and leadership fails over automatically.

| Role | startup catch-up | continuous fsnotify watcher | read-triggered catch-up | lease |
|---|---|---|---|---|
| **Leader** (holds lease) | yes | yes | not needed | renews periodically |
| **Follower** | yes | no | yes (before each read tool) | periodically tries to promote |

This satisfies "every session writes" while avoiding N redundant watchers at 4–8
concurrency, with robust failover.

## 4. Detailed design

### 4.1 Heartbeat lease (`internal/lock`)

Replace the PID-only lock with a lease file (same path, `config.LockPath()`):

- **Contents:** `PID` + `last-renewed unix timestamp` (e.g. two lines or
  space-separated).
- **TTL:** lease considered stale if `now - last_renewed > TTL` (default **15s**).
  Leader renews every `TTL/3` (default **5s**).
- **Liveness:** a follower may take over when the holder is stale **by either**:
  1. recorded PID is dead (existing signal-0 probe), **or**
  2. heartbeat older than TTL (covers a leader that is alive but hung — the
     reason the user asked for the heartbeat layer).
- **Atomic takeover:** reuse the existing "remove stale + `O_CREATE|O_EXCL`
  recreate" race-safe pattern. Two followers detecting staleness both attempt the
  exclusive create; only one wins, the loser re-reads, sees a fresh leader, and
  stays a follower.
- **API shape (new):**
  - `AcquireOrFollow(path) (*Lease, isLeader bool, err error)` — never fatal.
  - `(*Lease) Renew() error` — leader heartbeat.
  - `(*Lease) TryPromote() (bool, error)` — follower attempts takeover; true if
    it became leader.
  - `(*Lease) Release()` — leader removes the file on clean exit so a follower
    promotes immediately.
- **Backward compatibility:** an old-format file (PID only, no timestamp) is
  parsed tolerantly — liveness falls back to the PID probe alone. During an
  upgrade window a new follower respects a live old-format leader; when the old
  process exits and removes the file, the follower promotes.

### 4.2 Roles and lifecycle (`internal/cli/mcp.go`)

```
clio mcp starts
  → db.Open(path) in read-write mode (no fatal lock)
  → startup catch-up: ing.IngestAll(projects, false)   // IMMEDIATE + idempotent, concurrency-safe
  → lock.AcquireOrFollow(leasePath)
       ├─ isLeader → start watcher goroutine + lease-renew goroutine
       └─ follower → start a low-frequency promotion timer (default every 30s)
  → mcp.Serve(srv)   // serve tools (unchanged surface)
```

- **Leader:** runs `watcher.New(...).Run(ctx)` (live tail + 60s backstop) and a
  goroutine calling `lease.Renew()` every 5s.
- **Follower:** runs only a timer that calls `lease.TryPromote()`; on success it
  starts the watcher + renew goroutine and stops the promotion timer. This bounds
  the post-failover indexing gap to ≤ the promotion interval even when the
  follower is otherwise idle.

### 4.3 Follower freshness (read-triggered catch-up)

Followers do **not** run a continuous watcher. They stay fresh via:

1. The leader's watcher already indexes globally (≈500ms debounce), so a
   follower's own new messages are written to the shared DB by the leader.
2. Before serving any read tool, a follower runs a cheap **incremental
   catch-up** (reuse the existing `openForQuery()` / watermark logic;
   unchanged files are skipped via `ingest_state`). This closes the sub-second
   gap between "follower wrote its transcript to disk" and "leader ingested it",
   so query results are exactly fresh at query time.

Cost is bounded by human query frequency and is mostly cheap skips (the leader
has usually already ingested).

### 4.4 Cross-process write correctness (shared foundation)

Applies to leader and follower writes alike.

1. **IMMEDIATE transactions.** Add `_txlock=immediate` to the read-write DSN
   (`internal/db/db.go:34`). Confirmed supported by `modernc.org/sqlite@v1.50.1`
   (`driver.go:87`, `sqlite.go:195`). This takes the write lock at `BEGIN`,
   eliminating the stale-read race where two writers both read an old watermark
   and re-ingest the same delta. `busy_timeout(3000)` already retries on
   contention. (Read-only DSN unchanged.)

2. **Idempotent messages.** New migration `0003_*.sql`:
   - Dedup any pre-existing rows first:
     `DELETE FROM messages WHERE id NOT IN (SELECT MIN(id) FROM messages GROUP BY session_uuid, seq)`
     (must run before adding the constraint or it fails).
   - Add `CREATE UNIQUE INDEX ... ON messages(session_uuid, seq)`.
   - Change `internal/ingest/ingest.go:216` to `INSERT OR IGNORE INTO messages(...)`.
   - After the insert, check `res.RowsAffected()`:
     - `1` → use `res.LastInsertId()` for the dependent `tool_calls` inserts.
     - `0` (duplicate ignored) → the message and its tool_calls already exist;
       **skip** the tool_calls inserts (do not use the stale `LastInsertId`).

3. **FTS5 stays consistent automatically.** `messages_fts` is external-content
   with triggers (`0001_init.sql:31-49`). `INSERT OR IGNORE` that is ignored does
   not fire `messages_ai`, so FTS does not desync. The `changeFull`
   DELETE+reinsert path drives `messages_ad`/`messages_ai` normally.

4. **`SetMaxOpenConns(1)` retained** per process (serializes intra-process
   writes); cross-process serialization is SQLite's write lock + busy_timeout.

### 4.5 Why this is corruption-free (argument)

- **Incremental race:** writer A ingests seq X..Y (`OR IGNORE`), advances the
  watermark, commits. Writer B (planned X..Y from a stale read) waits on the
  IMMEDIATE write lock, then its `INSERT OR IGNORE` finds every (session_uuid,
  seq) already present → all `RowsAffected==0` → tool_calls skipped → watermark
  re-set to the same value → commit. No duplicates.
- **Full re-ingest race:** both classify `changeFull`; IMMEDIATE serializes them;
  each does DELETE-then-reinsert, so the final state is identical and the UNIQUE
  index holds within each transaction.

## 5. Error handling / edge cases

- **busy_timeout exceeded (>3s under load):** ingest returns an error and is
  retried on the next watcher tick / next read-triggered catch-up. Acceptable at
  4–8 concurrency.
- **Two followers promote simultaneously:** resolved by the atomic exclusive
  create (§4.1); loser stays follower.
- **Leader hung but PID alive:** heartbeat TTL triggers takeover (§4.1).
- **Clean leader exit:** `lease.Release()` removes the file → next follower
  promotion check (≤30s, or immediately on its next read) takes over.
- **Old-format lease during upgrade:** tolerated (§4.1).

## 6. Migration & backward compatibility

- `0003` migration is additive; the dedup step makes it safe on existing DBs.
- DSN `_txlock=immediate` is backward compatible.
- Lease file format change is backward compatible (tolerant parse).
- No change to MCP tool I/O.

## 7. Testing strategy

- **Lease unit tests:** acquire / renew / stale-by-PID / stale-by-heartbeat /
  two-follower CAS race. Use injectable clock + PID-liveness for determinism.
- **Concurrency integration test (key):** N goroutines and/or N subprocesses
  ingest the same continuously growing `.jsonl`; assert message count == expected
  (no duplicates), no errors, watermark correct. This proves multi-writer safety.
- **Idempotency test:** ingest the same file twice concurrently → stable counts;
  FTS row count matches messages.
- **Failover test:** start leader, kill it, assert a follower promotes and newly
  appended lines get indexed.
- **Read-triggered freshness test:** follower returns rows for content written
  after its startup catch-up.
- All existing tests must still pass.

## 8. Scope / YAGNI

- No daemon, socket IPC, or network.
- TTL/intervals hardcoded (15s TTL, 5s renew, 30s promotion poll).
- Tool surface and output formats unchanged.

## 9. File-by-file change map

- `internal/lock/lock.go` — lease (PID + heartbeat), `AcquireOrFollow`,
  `Renew`, `TryPromote`, tolerant parse.
- `internal/cli/mcp.go` — non-fatal acquire; leader vs follower wiring; renew &
  promotion goroutines.
- `internal/db/db.go` — add `_txlock=immediate` to the read-write DSN.
- `internal/db/migrations/0003_messages_unique.sql` — dedup + UNIQUE index.
- `internal/ingest/ingest.go` — `INSERT OR IGNORE` + `RowsAffected` handling for
  tool_calls.
- Follower read-path — read-triggered catch-up before MCP read tools (reuse
  `openForQuery()` logic; likely in `internal/mcp/tools.go` or a shared helper).
- `internal/cli/common.go` — review CLI call sites that use `lock.IsHeld()` /
  `openForQuery()` to defer to a running MCP writer; ensure they remain correct
  against the new lease (a live leader still reads as "held").
- Tests as in §7.
