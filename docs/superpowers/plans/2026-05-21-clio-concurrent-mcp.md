# clio Concurrent MCP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let multiple concurrent Claude Code sessions each run a working `clio mcp` server, with safe symmetric multi-writer ingestion and automatic, split-brain-free leader failover.

**Architecture:** Replace the fatal single-writer lock with a fenced heartbeat leader lease. Every instance opens the DB read-write and writes; one elected leader runs the `fsnotify` watcher (with failover); followers stay fresh via best-effort read-triggered catch-up. Reads are served from a read-only handle so they never block on the write lock. Cross-process write safety comes from `_txlock=immediate`, a `UNIQUE(session_uuid,seq)` index with `INSERT OR IGNORE`, in-transaction file re-validation (changeFull), a monotonic watermark guard (incremental), recomputed `turn_count`, and concurrency-safe migrations.

**Tech Stack:** Go 1.25, `modernc.org/sqlite` (pure-Go, WAL), `github.com/fsnotify/fsnotify`, `github.com/mark3labs/mcp-go`, `github.com/spf13/cobra`.

**Source spec:** `docs/superpowers/specs/2026-05-21-clio-concurrent-mcp-design.md`

**Branch:** `concurrent-mcp-lease`

**Commit convention:** every commit message ends with the trailer
`Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>` (repo convention). The
commit commands below omit it for brevity; add it when committing.

---

## File map

- `internal/db/db.go` — add `_txlock=immediate` to RW DSN; make `migrate()` concurrency-safe (per-migration IMMEDIATE txn + in-txn re-check + `INSERT OR IGNORE` marker).
- `internal/db/migrations/0003_messages_unique.sql` — dedup messages → delete orphan tool_calls → unique index.
- `internal/ingest/ingest.go` — `INSERT OR IGNORE` + `RowsAffected` for tool_calls; recompute `turn_count` via `COUNT(*)`; monotonic watermark for incremental, unconditional for full; in-txn re-validation for changeFull.
- `internal/lock/lock.go` — fenced heartbeat lease (`AcquireOrFollow`, `Renew`/`ErrSuperseded`, `TryPromote`, `Release`, `IsLeader`, `IsHeld`).
- `internal/lock/lock_test.go` — lease unit tests (injectable clock).
- `internal/mcp/server.go` + `internal/mcp/tools.go` — `NewServer` gains a `beforeRead func()` hook; each handler calls it.
- `internal/cli/mcp.go` — two handles (RW + RO); lease role manager (leader/follower, renew+demote, promotion poll); leader-only watcher + startup catch-up; throttled best-effort read catch-up.
- `internal/cli/common.go` — confirm `openForQuery()` still defers to a live leader under the new lease.
- `internal/ingest/concurrent_subprocess_test.go` + `cmd/clio-ingest-once` (test helper) — cross-process concurrency test.

---

## Task 1: Make transactions IMMEDIATE

**Files:**
- Modify: `internal/db/db.go:34`

- [ ] **Step 1: Add `_txlock=immediate` to the read-write DSN**

In `internal/db/db.go`, change the DSN in `Open` (line 34) from:

```go
	dsn := "file:" + path + "?_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
```

to:

```go
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
```

Leave `OpenReadOnly` (line 64) unchanged.

- [ ] **Step 2: Verify the suite still passes**

Run: `go test ./...`
Expected: PASS (no behavior change for single-process tests; `BEGIN` is now `BEGIN IMMEDIATE`).

- [ ] **Step 3: Commit**

```bash
git add internal/db/db.go
git commit -m "feat(db): use BEGIN IMMEDIATE for write transactions"
```

---

## Task 2: Concurrency-safe migrations + migration 0003

**Files:**
- Create: `internal/db/migrations/0003_messages_unique.sql`
- Modify: `internal/db/db.go:79-116` (`migrate`)
- Test: `internal/db/db_test.go`

- [ ] **Step 1: Write migration 0003**

Create `internal/db/migrations/0003_messages_unique.sql`:

```sql
-- Dedup any pre-existing duplicate (session_uuid, seq) rows, keeping the lowest
-- id. The AFTER DELETE trigger keeps messages_fts in sync.
DELETE FROM messages
WHERE id NOT IN (SELECT MIN(id) FROM messages GROUP BY session_uuid, seq);

-- Remove tool_calls orphaned by the dedup (no FK/cascade exists).
DELETE FROM tool_calls WHERE message_id NOT IN (SELECT id FROM messages);

-- Enforce idempotent message ingestion under concurrent writers.
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq
    ON messages(session_uuid, seq);
```

- [ ] **Step 2: Write the failing test for concurrency-safe migrate**

Add to `internal/db/db_test.go`:

```go
func TestMigrateIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")

	// Pre-create the DB so the file exists, then run migrate() from many
	// goroutines against independent connections to the same file. With the
	// per-migration IMMEDIATE txn + in-txn re-check, none should fail.
	seed, err := Open(path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	seed.Close()

	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := Open(path) // Open runs migrate()
			if err != nil {
				errs <- err
				return
			}
			errs <- d.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent migrate failed: %v", e)
		}
	}
}
```

Add `"sync"` and `"path/filepath"` to the test imports if missing.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/db/ -run TestMigrateIsConcurrencySafe -v`
Expected: FAIL — one goroutine hits `UNIQUE constraint failed: schema_migrations.name` (the current `migrate()` has no cross-process/cross-connection guard).

- [ ] **Step 4: Make `migrate()` concurrency-safe**

Replace the per-migration apply block in `internal/db/db.go` (the `for _, name := range names { ... }` loop, lines ~96-114) with a transactional version that re-checks inside the transaction:

```go
	for _, name := range names {
		applied, err := d.migrationApplied(name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		stmts, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := d.applyMigration(name, string(stmts)); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) migrationApplied(name string) (bool, error) {
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("check migration %s: %w", name, err)
	}
	return n > 0, nil
}

// applyMigration runs one migration inside an IMMEDIATE transaction. It
// re-checks the marker inside the transaction so a concurrent process that
// already applied it is a clean no-op rather than a primary-key failure.
func (d *DB) applyMigration(name, stmts string) error {
	tx, err := d.Begin() // IMMEDIATE via _txlock=immediate
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
		return fmt.Errorf("recheck migration %s: %w", name, err)
	}
	if n > 0 {
		return tx.Commit() // applied by someone else while we waited for the lock
	}
	if _, err := tx.Exec(stmts); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations(name, applied_at) VALUES (?, strftime('%s','now'))`, name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	return tx.Commit()
}
```

Keep the `CREATE TABLE IF NOT EXISTS schema_migrations` and the directory-listing/sort code above the loop unchanged.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/db/ -run TestMigrateIsConcurrencySafe -v`
Expected: PASS.

- [ ] **Step 6: Run the full DB package and confirm 0003 applies**

Run: `go test ./internal/db/ -v`
Expected: PASS (existing migration/schema tests still green; 0003 applied).

- [ ] **Step 7: Commit**

```bash
git add internal/db/db.go internal/db/migrations/0003_messages_unique.sql internal/db/db_test.go
git commit -m "feat(db): concurrency-safe migrations + 0003 unique(session_uuid,seq)"
```

---

## Task 3: Idempotent message inserts

**Files:**
- Modify: `internal/ingest/ingest.go:192-253` (`commit`)
- Test: `internal/ingest/ingest_test.go`

- [ ] **Step 1: Write the failing test for duplicate-safe re-ingest**

Add to `internal/ingest/ingest_test.go`. It uses the helpers already at the top of
that file: `writeSession(t, root, encodedDir, uuid, lines...)`, `openTestDB(t)`,
and the `evUser1`/`evAsst1` event constants.

```go
func TestIngestForceTwiceNoDuplicates(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	ing := New(database, nil)

	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if _, _, err := ing.IngestFile(path, true); err != nil { // force full re-ingest again
		t.Fatalf("second ingest: %v", err)
	}

	var msgCount, ftsCount int
	if err := database.QueryRow(`SELECT count(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&ftsCount); err != nil {
		t.Fatal(err)
	}
	if msgCount == 0 {
		t.Fatal("no messages ingested")
	}
	if ftsCount != msgCount {
		t.Fatalf("fts rows = %d, messages = %d (must match)", ftsCount, msgCount)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/ingest/ -run TestIngestForceTwiceNoDuplicates -v`
Expected: PASS already for the force/full path (changeFull deletes first), but it pins the FTS==messages invariant. If it FAILS on count, continue; the OR IGNORE change below is still required for the concurrent path covered in Task 11.

- [ ] **Step 3: Switch the message insert to `INSERT OR IGNORE` and branch on RowsAffected**

In `internal/ingest/ingest.go` `commit`, replace the message-insert loop (lines ~214-235) with:

```go
	userTurns := 0
	inserted := 0
	for _, m := range msgs {
		res, err := tx.Exec(`INSERT OR IGNORE INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
			m.SessionUUID, m.Seq, nullZero(m.TS), m.Role, m.Content, m.RawJSON)
		if err != nil {
			return 0, fmt.Errorf("insert message: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected == 0 {
			// Duplicate (session_uuid, seq) already present (concurrent writer or
			// re-ingest). Its tool_calls already exist; do not use the stale
			// LastInsertId.
			continue
		}
		inserted++
		if m.Role == model.RoleUser {
			userTurns++
		}
		if len(m.ToolCalls) > 0 {
			id, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			for _, tc := range m.ToolCalls {
				if _, err := tx.Exec(`INSERT INTO tool_calls(message_id, tool_name, params_summary) VALUES (?,?,?)`, id, tc.ToolName, tc.ParamsSummary); err != nil {
					return 0, err
				}
			}
		}
	}
```

Keep the existing `ing.upsertSession(tx, sess, userTurns, kind)` call (Task 4
replaces how turn_count is derived). Then change the function's return at the end
(line ~252) from `return len(msgs), nil` to `return inserted, nil`.

- [ ] **Step 4: Run ingest tests**

Run: `go test ./internal/ingest/ -v`
Expected: PASS. (`TestIngestForceTwiceNoDuplicates` green; existing tests still green.)

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/ingest.go internal/ingest/ingest_test.go
git commit -m "feat(ingest): idempotent message inserts via INSERT OR IGNORE"
```

---

## Task 4: Recompute turn_count (no drift)

**Files:**
- Modify: `internal/ingest/ingest.go:192-281` (`commit` + `upsertSession`)
- Test: `internal/ingest/ingest_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/ingest/ingest_test.go`:

```go
func TestTurnCountStableAcrossReingest(t *testing.T) {
	projects := t.TempDir()
	// evUser1 + evUser2 are the two user turns; evAsst1 is the assistant reply.
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)

	// Force-ingest the same file twice. turn_count must equal the number of
	// user messages (2), not double.
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}

	var turns int
	if err := database.QueryRow(`SELECT turn_count FROM sessions WHERE uuid = 'sess-1'`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("turn_count = %d, want 2", turns)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/ingest/ -run TestTurnCountStableAcrossReingest -v`
Expected: PASS for force/full (full re-parse sets count), but the incremental `+=` path is still wrong — this test pins the invariant; the fix below makes the incremental path correct too (covered under concurrency in Task 11).

- [ ] **Step 3: Compute the authoritative turn_count inside the transaction**

First, delete the now-unused `userTurns` counting from the message loop (added in
Task 3): remove the `userTurns := 0` line and the `if m.Role == model.RoleUser {
userTurns++ }` block. Keep `inserted`.

Then replace the existing `ing.upsertSession(tx, sess, userTurns, kind)` call with
a COUNT-based authoritative value:

```go
	var totalUserTurns int
	if err := tx.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid = ? AND role = ?`,
		sess.UUID, model.RoleUser).Scan(&totalUserTurns); err != nil {
		return 0, fmt.Errorf("count user turns: %w", err)
	}
	if err := ing.upsertSession(tx, sess, totalUserTurns, kind); err != nil {
		return 0, err
	}
```

- [ ] **Step 4: Make `upsertSession` set turn_count absolutely (not `+=`)**

In `upsertSession` (lines ~255-281), the `changeFull` branch already does
`turn_count=excluded.turn_count`; keep it (now fed the authoritative count).
Change the incremental branch's `UPDATE` from `turn_count = turn_count + ?` to an
absolute set:

```go
	// Incremental: bump ended_at, set turn_count to the authoritative count,
	// fill project_path/title if missing.
	_, err := tx.Exec(`UPDATE sessions SET
		ended_at = MAX(COALESCE(ended_at,0), ?),
		turn_count = ?,
		project_path = COALESCE(NULLIF(project_path,''), ?),
		title = COALESCE(NULLIF(title,''), ?)
		WHERE uuid = ?`,
		nullZero(s.EndedAt), userTurns, nullEmpty(s.ProjectPath), nullEmpty(s.Title), s.UUID)
```

(The parameter is still named `userTurns` in the `upsertSession` signature; it now carries the authoritative total. Keep the signature `func (ing *Ingester) upsertSession(tx *sql.Tx, s model.Session, userTurns int, kind changeKind) error`.)

- [ ] **Step 5: Run ingest tests**

Run: `go test ./internal/ingest/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/ingest.go internal/ingest/ingest_test.go
git commit -m "fix(ingest): recompute turn_count via COUNT(*) to avoid concurrent drift"
```

---

## Task 5: Monotonic watermark (incremental) + unconditional (full)

**Files:**
- Modify: `internal/ingest/ingest.go:241-247` (the `ingest_state` upsert in `commit`)
- Test: `internal/ingest/ingest_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/ingest/ingest_test.go`:

```go
func TestIncrementalWatermarkIsMonotonic(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, _, err := ing.IngestFile(path, false); err != nil {
		t.Fatal(err)
	}

	var before int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file = ?`, path).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// Simulate a stale incremental writer trying to push the offset backward.
	_, err := database.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(source_file) DO UPDATE SET last_byte_offset=excluded.last_byte_offset
		WHERE excluded.last_byte_offset >= ingest_state.last_byte_offset`,
		path, 1, 1, before-10, "x", 1)
	if err != nil {
		t.Fatal(err)
	}

	var after int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file = ?`, path).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("watermark moved backward: before=%d after=%d", before, after)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/ingest/ -run TestIncrementalWatermarkIsMonotonic -v`
Expected: PASS (this test exercises the guarded SQL directly). It documents the
guard the production code must use.

- [ ] **Step 3: Apply the guard for incremental, keep full unconditional**

In `commit`, replace the single `ingest_state` upsert (lines ~241-247) with a
kind-aware version. `changeFull` must be allowed to reset the offset downward
(truncation/rewrite); incremental must never regress:

```go
	if kind == changeFull {
		if _, err := tx.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(source_file) DO UPDATE SET last_size=excluded.last_size, last_mtime=excluded.last_mtime,
			last_byte_offset=excluded.last_byte_offset, tail_fingerprint=excluded.tail_fingerprint, last_ingested_at=excluded.last_ingested_at`,
			fs.SourceFile, fs.LastSize, fs.LastMTime, fs.LastByteOffset, fs.TailFingerprint, fs.LastIngestedAt); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(source_file) DO UPDATE SET last_size=excluded.last_size, last_mtime=excluded.last_mtime,
			last_byte_offset=excluded.last_byte_offset, tail_fingerprint=excluded.tail_fingerprint, last_ingested_at=excluded.last_ingested_at
			WHERE excluded.last_byte_offset >= ingest_state.last_byte_offset`,
			fs.SourceFile, fs.LastSize, fs.LastMTime, fs.LastByteOffset, fs.TailFingerprint, fs.LastIngestedAt); err != nil {
			return 0, err
		}
	}
```

- [ ] **Step 4: Run ingest tests**

Run: `go test ./internal/ingest/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/ingest.go internal/ingest/ingest_test.go
git commit -m "feat(ingest): monotonic watermark for incremental, unconditional for full"
```

---

## Task 6: In-transaction file re-validation (no DB-older-than-disk)

**Files:**
- Modify: `internal/ingest/ingest.go` (`commit` start; `IngestFile` error handling)
- Test: `internal/ingest/ingest_test.go`

- [ ] **Step 1: Write the failing test**

This simulates the reversion race deterministically: capture a full snapshot of
V1, replace the file with V2 on disk, then attempt to commit V1 — the commit must
abort rather than revert the DB below disk.

Add a test that drives `commit` indirectly via a helper. Since `commit` is
unexported, test through `IngestFile` with a swap in between using a hook. Add a
package-level test seam in `ingest.go`:

```go
// preCommitHook, if non-nil, runs inside commit() just after BEGIN and before
// any writes. Tests use it to mutate the on-disk file and exercise the
// re-validation guard. Always nil in production.
var preCommitHook func()
```

Then the test:

```go
func TestChangeFullAbortsWhenFileChangedUnderUs(t *testing.T) {
	projects := t.TempDir()
	// V1 = two user events only (2 messages).
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)

	// Seed V1.
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}

	// Arrange: while the next full ingest is mid-transaction (after BEGIN, before
	// writes), replace the file with V2 (four events, different size) on disk to
	// simulate a concurrent writer's commit.
	preCommitHook = func() {
		writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1, evResult1, evUser2)
		preCommitHook = nil
	}
	t.Cleanup(func() { preCommitHook = nil })

	// Force a full ingest using the stale (V1) in-memory snapshot. It must abort
	// cleanly (no error surfaced) rather than revert the DB to V1 over V2.
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatalf("ingest should abort cleanly, got error: %v", err)
	}

	// A subsequent ingest reconciles the DB to V2 (more messages than V1's 2).
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n <= 2 {
		t.Fatalf("messages = %d; expected reconcile to V2 (>2)", n)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/ingest/ -run TestChangeFullAbortsWhenFileChangedUnderUs -v`
Expected: FAIL — without the guard, the V1 full ingest deletes+reinserts V1 over V2.

- [ ] **Step 3: Add the re-validation guard in `commit`**

At the top of `commit`, right after `defer tx.Rollback()`, add:

```go
	if preCommitHook != nil {
		preCommitHook()
	}

	// changeFull deletes the session's rows before reinserting. If the file
	// changed since we read it (a concurrent writer committed newer bytes),
	// committing our stale snapshot would revert the DB below disk. Re-stat
	// inside the write lock and abort if it moved.
	if kind == changeFull {
		if fi, statErr := os.Stat(fs.SourceFile); statErr == nil {
			if fi.Size() != fs.LastSize || fi.ModTime().Unix() != fs.LastMTime {
				return 0, errStaleSnapshot
			}
		}
	}
```

Add the sentinel near the top of the file (with the other vars):

```go
// errStaleSnapshot means the source file changed between read and commit; the
// caller treats it as a no-op and lets a later pass re-ingest the fresh bytes.
var errStaleSnapshot = errors.New("source file changed during ingest")
```

Ensure `"os"` and `"errors"` are imported in `ingest.go` (they already are).

- [ ] **Step 4: Treat the sentinel as a clean skip in `IngestFile`**

In `IngestFile`, where it calls `ing.commit(...)` (around line ~1015 in the full
file / the `n, err := ing.commit(...)` call), change the error handling from:

```go
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
```

to:

```go
	if err != nil {
		if errors.Is(err, errStaleSnapshot) {
			return 0, false, nil // changed under us; next pass will re-ingest
		}
		return 0, false, err
	}
	return n, true, nil
```

- [ ] **Step 5: Run the test**

Run: `go test ./internal/ingest/ -run TestChangeFullAbortsWhenFileChangedUnderUs -v`
Expected: PASS.

- [ ] **Step 6: Run the ingest suite**

Run: `go test ./internal/ingest/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/ingest/ingest.go internal/ingest/ingest_test.go
git commit -m "fix(ingest): abort changeFull when source file changed under us"
```

---

## Task 7: Fenced heartbeat lease

**Files:**
- Rewrite: `internal/lock/lock.go`
- Create: `internal/lock/lock_test.go` (extend if it exists)

- [ ] **Step 1: Write failing lease unit tests**

Create/replace `internal/lock/lock_test.go`:

```go
package lock

import (
	"path/filepath"
	"testing"
	"time"
)

func leaseAt(t *testing.T, path string, now *time.Time) *Lease {
	t.Helper()
	l := newLease(path, 10*time.Second, func() time.Time { return *now })
	return l
}

func TestAcquireThenFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)

	a := leaseAt(t, path, &now)
	ok, err := a.TryPromote()
	if err != nil || !ok {
		t.Fatalf("A should become leader: ok=%v err=%v", ok, err)
	}

	b := leaseAt(t, path, &now)
	ok, err = b.TryPromote()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("B should follow a live leader, not promote")
	}
}

func TestRenewKeepsLeadership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)
	a := leaseAt(t, path, &now)
	if ok, _ := a.TryPromote(); !ok {
		t.Fatal("A should lead")
	}
	now = now.Add(20 * time.Second) // past TTL
	if err := a.Renew(); err != nil {
		t.Fatalf("owner renew should succeed: %v", err)
	}
}

func TestStaleHeartbeatAllowsTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)
	a := leaseAt(t, path, &now)
	if ok, _ := a.TryPromote(); !ok {
		t.Fatal("A should lead")
	}
	now = now.Add(20 * time.Second) // A's heartbeat is now stale
	b := leaseAt(t, path, &now)
	if ok, err := b.TryPromote(); err != nil || !ok {
		t.Fatalf("B should take over a stale lease: ok=%v err=%v", ok, err)
	}
	// Fencing: A's renew must now report it was superseded.
	if err := a.Renew(); err != ErrSuperseded {
		t.Fatalf("A.Renew after takeover = %v, want ErrSuperseded", err)
	}
	// And A's Release must not delete B's lease.
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if !IsHeld(path) {
		t.Fatal("B's lease should still be held after A.Release")
	}
}

func TestReleaseByOwnerRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)
	a := leaseAt(t, path, &now)
	if ok, _ := a.TryPromote(); !ok {
		t.Fatal("A should lead")
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if IsHeld(path) {
		t.Fatal("lease should be released")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/lock/ -v`
Expected: FAIL to compile — `newLease`, `TryPromote`, `ErrSuperseded`, `Lease` (new shape) don't exist yet.

- [ ] **Step 3: Rewrite `internal/lock/lock.go`**

```go
// Package lock implements a fenced heartbeat leader lease so multiple clio mcp
// processes can coordinate a single watcher with safe, split-brain-free
// failover. The lease file holds "pid nonce unix-seconds". Ownership is fenced
// by the nonce: a superseded leader's Renew/Release become no-ops.
package lock

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrSuperseded means another process took leadership; the caller must demote.
var ErrSuperseded = errors.New("lease superseded by another leader")

// DefaultTTL is how long a heartbeat is considered fresh.
const DefaultTTL = 10 * time.Second

// Lease represents this process's participation in leader election.
type Lease struct {
	path  string
	pid   int
	nonce uint64
	ttl   time.Duration
	now   func() time.Time
	owned bool
}

type record struct {
	pid   int
	nonce uint64
	ts    int64
}

func newLease(path string, ttl time.Duration, now func() time.Time) *Lease {
	return &Lease{path: path, pid: os.Getpid(), ttl: ttl, now: now}
}

// AcquireOrFollow opens the lease at path, becoming leader if it is absent or
// stale, otherwise following. Never fatal.
func AcquireOrFollow(path string) (*Lease, bool, error) {
	l := newLease(path, DefaultTTL, time.Now)
	ok, err := l.TryPromote()
	if err != nil {
		return nil, false, err
	}
	return l, ok, nil
}

// IsLeader reports whether this process currently owns the lease.
func (l *Lease) IsLeader() bool { return l != nil && l.owned }

// TryPromote takes leadership if the current lease is absent or stale. Returns
// true if this process is now the leader.
func (l *Lease) TryPromote() (bool, error) {
	rec, _ := readRecord(l.path)
	if rec != nil && l.live(rec) && rec.nonce != l.nonce {
		return false, nil
	}
	nonce := rand.Uint64()
	for nonce == 0 {
		nonce = rand.Uint64()
	}
	if err := writeRecordAtomic(l.path, l.pid, nonce, l.now().Unix()); err != nil {
		return false, err
	}
	after, err := readRecord(l.path)
	if err != nil || after == nil || after.nonce != nonce {
		l.owned = false
		return false, nil // lost a takeover race
	}
	l.nonce, l.owned = nonce, true
	return true, nil
}

// Renew refreshes the heartbeat, or returns ErrSuperseded if another process
// has taken over (the caller must stop its watcher and demote).
func (l *Lease) Renew() error {
	if !l.owned {
		return ErrSuperseded
	}
	rec, err := readRecord(l.path)
	if err != nil || rec == nil || rec.nonce != l.nonce {
		l.owned = false
		return ErrSuperseded
	}
	return writeRecordAtomic(l.path, l.pid, l.nonce, l.now().Unix())
}

// Release removes the lease only if this process still owns it.
func (l *Lease) Release() error {
	if l == nil || !l.owned {
		return nil
	}
	rec, err := readRecord(l.path)
	if err != nil || rec == nil || rec.nonce != l.nonce {
		l.owned = false
		return nil
	}
	l.owned = false
	return os.Remove(l.path)
}

func (l *Lease) live(rec *record) bool {
	if rec.pid != os.Getpid() && !pidAlive(rec.pid) {
		return false
	}
	return l.now().Unix()-rec.ts <= int64(l.ttl/time.Second)
}

// IsHeld reports whether a live (non-stale) leader currently holds the lease.
// Used by CLI commands to defer to a running MCP writer.
func IsHeld(path string) bool {
	rec, err := readRecord(path)
	if err != nil || rec == nil {
		return false
	}
	if rec.pid != os.Getpid() && !pidAlive(rec.pid) {
		return false
	}
	return time.Now().Unix()-rec.ts <= int64(DefaultTTL/time.Second)
}

func readRecord(path string) (*record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := strings.Fields(strings.TrimSpace(string(data)))
	if len(f) != 3 {
		return nil, nil // malformed/old-format -> treated as stale
	}
	pid, e1 := strconv.Atoi(f[0])
	nonce, e2 := strconv.ParseUint(f[1], 10, 64)
	ts, e3 := strconv.ParseInt(f[2], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || pid <= 0 {
		return nil, nil
	}
	return &record{pid: pid, nonce: nonce, ts: ts}, nil
}

func writeRecordAtomic(path string, pid int, nonce uint64, ts int64) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp.lock.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(fmt.Sprintf("%d %d %d", pid, nonce, ts)); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
```

- [ ] **Step 4: Run the lease tests**

Run: `go test ./internal/lock/ -v`
Expected: PASS.

- [ ] **Step 5: Build the whole module (catch callers of the old `Acquire`)**

Run: `go build ./...`
Expected: FAIL in `internal/cli/mcp.go` (it calls the removed `lock.Acquire`). That
is wired up in Task 9. `common.go` uses `lock.IsHeld`, which still exists.

- [ ] **Step 6: Commit**

```bash
git add internal/lock/lock.go internal/lock/lock_test.go
git commit -m "feat(lock): fenced heartbeat leader lease with failover"
```

---

## Task 8: Read hook on the MCP server

**Files:**
- Modify: `internal/mcp/server.go` (`NewServer` signature) and `internal/mcp/tools.go` (call the hook)
- Modify: any test/caller of `NewServer`

- [ ] **Step 1: Add a `beforeRead` hook to `NewServer`**

In `internal/mcp/server.go`, change the signature and thread the hook into each
handler:

```go
// NewServer builds an MCP server with clio's four tools registered. beforeRead,
// if non-nil, runs before each read tool serves (used by followers for a
// best-effort catch-up). It must never block indefinitely or panic.
func NewServer(database *db.DB, version string, beforeRead func()) *server.MCPServer {
```

Update the four `AddTool` handler calls to pass the hook:

```go
	), handleSearch(database, beforeRead))
	...
	), handleListSessions(database, beforeRead))
	...
	), handleActivitySummary(database, beforeRead))
	...
	), handleReadSession(database, beforeRead))
```

- [ ] **Step 2: Call the hook in each handler**

In `internal/mcp/tools.go`, add a `beforeRead func()` parameter to each
`handleX` function and invoke it at the top of the returned closure. Example for
`handleSearch` (apply the same two edits to the other three):

```go
func handleSearch(database *db.DB, beforeRead func()) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if beforeRead != nil {
			beforeRead()
		}
		query, err := req.RequireString("query")
		...
```

Do the same for `handleListSessions`, `handleActivitySummary`, `handleReadSession`
(add the param; call `beforeRead()` first thing in the closure).

- [ ] **Step 3: Fix callers**

Run: `grep -rn "NewServer(" internal cmd`
Update each call site (production is `internal/cli/mcp.go`, handled in Task 9; any
test calls become `NewServer(db, version, nil)`).

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: FAIL only in `internal/cli/mcp.go` (Task 9). The `internal/mcp` package
compiles.

Run: `go test ./internal/mcp/ -v`
Expected: PASS (with any test caller updated to pass `nil`).

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/server.go internal/mcp/tools.go
git commit -m "feat(mcp): add beforeRead hook to read tools"
```

---

## Task 9: Wire roles in `clio mcp`

**Files:**
- Modify: `internal/cli/mcp.go`

- [ ] **Step 1: Replace `newMCPCmd`'s `RunE` with the role-aware version**

Rewrite the body of `RunE` in `internal/cli/mcp.go`:

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			log := stderrLogger()

			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			// RW handle: migrations + all writes (watcher + catch-up).
			rw, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer rw.Close()
			// RO handle: serves read tools; never blocks on the write lock.
			ro, err := db.OpenReadOnly(dbPath)
			if err != nil {
				return err
			}
			defer ro.Close()

			lockPath, err := config.LockPath()
			if err != nil {
				return err
			}
			lease, isLeader, err := lock.AcquireOrFollow(lockPath)
			if err != nil {
				return fmt.Errorf("acquire lease: %w", err)
			}
			defer lease.Release()

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			projects, _ := config.ClaudeProjectsDir()
			ing := ingest.New(rw, log)

			// Best-effort, throttled read catch-up for followers (and harmless for
			// leaders, who are kept fresh by the watcher). Never fails a read.
			var catchupMu sync.Mutex
			var lastCatchup time.Time
			beforeRead := func() {
				if projects == "" {
					return
				}
				catchupMu.Lock()
				if time.Since(lastCatchup) < time.Second {
					catchupMu.Unlock()
					return
				}
				lastCatchup = time.Now()
				catchupMu.Unlock()
				if _, err := ing.IngestAll(projects, false); err != nil {
					log.Warn("read catch-up failed (serving possibly-stale)", "err", err)
				}
			}

			go runLeaseRole(ctx, lease, isLeader, ing, projects, log)

			log.Info("clio mcp server starting", "leader", isLeader)
			srv := mcp.NewServer(ro, version, beforeRead)
			return mcp.Serve(srv)
		},
```

- [ ] **Step 2: Add the role manager helpers (same file)**

Append to `internal/cli/mcp.go`:

```go
// runLeaseRole drives leader/follower transitions until ctx is cancelled.
func runLeaseRole(ctx context.Context, lease *lock.Lease, isLeader bool, ing *ingest.Ingester, projects string, log *slog.Logger) {
	for {
		if !isLeader {
			if !pollUntilLeader(ctx, lease, log) {
				return // ctx done
			}
		}
		// Now leader. leaderLoop returns true if superseded (demote), false if ctx done.
		if !leaderLoop(ctx, lease, ing, projects, log) {
			return
		}
		isLeader = false
	}
}

// pollUntilLeader polls TryPromote every 5s. Returns true once promoted, false
// if ctx is cancelled.
func pollUntilLeader(ctx context.Context, lease *lock.Lease, log *slog.Logger) bool {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			ok, err := lease.TryPromote()
			if err != nil {
				log.Warn("promote attempt failed", "err", err)
				continue
			}
			if ok {
				return true
			}
		}
	}
}

// leaderLoop runs the watcher + 3s renew while leader. Returns true if the lease
// was superseded (caller demotes), false if ctx was cancelled.
func leaderLoop(ctx context.Context, lease *lock.Lease, ing *ingest.Ingester, projects string, log *slog.Logger) bool {
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()

	if projects != "" {
		if _, err := os.Stat(projects); err == nil {
			if _, err := ing.IngestAll(projects, false); err != nil {
				log.Warn("startup catch-up failed", "err", err)
			}
			go func() {
				if err := watcher.New(ing, projects, log).Run(wctx); err != nil {
					log.Warn("watcher stopped", "err", err)
				}
			}()
		}
	}

	renew := time.NewTicker(3 * time.Second)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-renew.C:
			if err := lease.Renew(); err != nil {
				if errors.Is(err, lock.ErrSuperseded) {
					log.Warn("lease superseded; demoting to follower")
					return true
				}
				log.Warn("lease renew failed", "err", err)
			}
		}
	}
}
```

- [ ] **Step 3: Fix imports**

Ensure `internal/cli/mcp.go` imports: `context`, `errors`, `fmt`, `log/slog`,
`os`, `sync`, `time`, plus `config`, `db`, `ingest`, `lock`, `mcp`, `watcher`,
and `cobra`. Remove now-unused imports if any.

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Manual smoke test (two writers)**

```bash
go build -o /tmp/clio ./cmd/clio
# terminal-free smoke: start two servers briefly against the real index.
/tmp/clio mcp & P1=$!; /tmp/clio mcp & P2=$!
sleep 2
kill $P1 $P2 2>/dev/null
```
Expected: neither process prints "another clio mcp server is already running";
both exit cleanly on kill. (Functional MCP I/O is covered by tests; this only
confirms both start.)

- [ ] **Step 7: Commit**

```bash
git add internal/cli/mcp.go
git commit -m "feat(mcp): symmetric multi-writer with leader lease + failover"
```

---

## Task 10: Confirm CLI defers to a live leader

**Files:**
- Modify (if needed): `internal/cli/common.go`
- Test: `internal/cli/common_test.go` (create if absent)

- [ ] **Step 1: Write a test that `openForQuery` opens read-only when a lease is held**

Create/extend `internal/cli/common_test.go`:

```go
package cli

import (
	"testing"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/lock"
)

func TestOpenForQueryDefersToLiveLeader(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if _, err := config.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	dbPath, err := config.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	// Seed the index so openForQuery doesn't error on "no index".
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()

	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	lease, isLeader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !isLeader {
		t.Fatalf("expected to lead: leader=%v err=%v", isLeader, err)
	}
	defer lease.Release()

	if !lock.IsHeld(lockPath) {
		t.Fatal("lock should read as held while a live leader exists")
	}
	// openForQuery should defer to the leader and return a usable RO handle.
	d, err := openForQuery()
	if err != nil {
		t.Fatalf("openForQuery: %v", err)
	}
	defer d.Close()
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/cli/ -run TestOpenForQueryDefersToLiveLeader -v`
Expected: PASS — `openForQuery` already branches on `lock.IsHeld` (`common.go:436`),
and the new `IsHeld` reports a live lease as held. No code change expected.

- [ ] **Step 3: If the test fails, align `common.go`**

Only if needed: confirm `openForQuery` uses `lock.IsHeld(lockPath)` and returns
`db.OpenReadOnly` when held. No signature change is expected; the new lease keeps
`IsHeld`.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/common.go internal/cli/common_test.go
git commit -m "test(cli): openForQuery defers to a live leader under the new lease"
```

---

## Task 11: Cross-process concurrency integration test (capstone)

**Files:**
- Create: `cmd/clio-ingest-once/main.go` (test helper binary)
- Create: `internal/ingest/concurrent_subprocess_test.go`
- Modify/replace: `internal/ingest/ingest_test.go:188` (`TestConcurrentReadDuringWrite` — keep, but note it does not prove cross-process safety)

> Why a subprocess: goroutines share one `*sql.DB` (and `SetMaxOpenConns(1)`),
> so they exercise neither cross-process SQLite file locking nor lease races.
> Only real OS processes with independent connections hit the actual bugs.

- [ ] **Step 1: Write the helper binary**

Create `cmd/clio-ingest-once/main.go`:

```go
// Command clio-ingest-once is a test-only helper: it opens the DB at $CLIO_DB and
// runs IngestAll over $CLIO_PROJECTS once, then exits. Used by the cross-process
// concurrency test to spawn real competing writers.
package main

import (
	"fmt"
	"os"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

func main() {
	dbPath := os.Getenv("CLIO_DB")
	projects := os.Getenv("CLIO_PROJECTS")
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer d.Close()
	if _, err := ingest.New(d, nil).IngestAll(projects, false); err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Write the failing cross-process test**

Create `internal/ingest/concurrent_subprocess_test.go`:

```go
package ingest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

// buildHelper compiles cmd/clio-ingest-once into a temp binary.
func buildHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "clio-ingest-once")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/linhn0617/clio/cmd/clio-ingest-once").CombinedOutput()
	if err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	return bin
}

func TestCrossProcessConcurrentIngest(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped in -short")
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "db.sqlite")
	projects := filepath.Join(root, "projects", "proj")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}

	// One growing session file with N user + N assistant lines.
	uuid := "55555555-5555-5555-5555-555555555555"
	file := filepath.Join(projects, uuid+".jsonl")
	writeGrowingSession(t, file, uuid, 200) // helper: 200 user + 200 assistant lines

	bin := buildHelper(t)

	const procs = 6
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(bin)
			cmd.Env = append(os.Environ(), "CLIO_DB="+dbPath, "CLIO_PROJECTS="+filepath.Dir(projects))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("helper failed: %v\n%s", err, out)
			}
		}()
	}
	wg.Wait()

	d, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// No duplicate (session_uuid, seq).
	var dupes int
	if err := d.QueryRow(`SELECT COUNT(*) FROM (SELECT session_uuid, seq, COUNT(*) c FROM messages GROUP BY session_uuid, seq HAVING c > 1)`).Scan(&dupes); err != nil {
		t.Fatal(err)
	}
	if dupes != 0 {
		t.Fatalf("found %d duplicate (session_uuid,seq) groups", dupes)
	}

	// FTS row count == messages row count.
	var msgs, fts int
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages_fts`).Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if msgs != fts {
		t.Fatalf("messages=%d fts=%d (must match)", msgs, fts)
	}
	if msgs != 400 {
		t.Fatalf("messages=%d, want 400", msgs)
	}

	// turn_count == number of user messages (no drift).
	var turns, userMsgs int
	if err := d.QueryRow(`SELECT turn_count FROM sessions WHERE uuid=?`, uuid).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userMsgs); err != nil {
		t.Fatal(err)
	}
	if turns != userMsgs {
		t.Fatalf("turn_count=%d, user messages=%d (drift)", turns, userMsgs)
	}
}
```

- [ ] **Step 3: Add the `writeGrowingSession` helper**

In the same test file, add a helper that writes a valid clio session `.jsonl`.
The line shapes match `internal/ingest/ingest_test.go`'s `evUser1`/`evAsst1`
constants (the format the parser accepts). Add `"fmt"` to the imports.

```go
func userJSONLine(uuid string, i int) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/tmp/p","sessionId":%q,"message":{"role":"user","content":"user message %d"}}`, uuid, i)
}

func assistantJSONLine(uuid string, i int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":%q,"message":{"role":"assistant","content":[{"type":"text","text":"assistant reply %d"}]}}`, uuid, i)
}

func writeGrowingSession(t *testing.T, path, uuid string, pairs int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := 0; i < pairs; i++ {
		if _, err := fmt.Fprintln(f, userJSONLine(uuid, i)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(f, assistantJSONLine(uuid, i)); err != nil {
			t.Fatal(err)
		}
	}
}
```

The session's canonical uuid is the filename (`<uuid>.jsonl`), so `messages.session_uuid`
equals `uuid` regardless of the `sessionId` field.

- [ ] **Step 4: Run the cross-process test to verify it fails on an unhardened build**

(Optional sanity: `git stash` the Task 3-6 changes, run, observe drift/dupes, then
`git stash pop`.) Otherwise:

Run: `go test ./internal/ingest/ -run TestCrossProcessConcurrentIngest -v`
Expected: PASS on the hardened build — no dupes, FTS==messages, turn_count exact.

- [ ] **Step 5: Annotate the weak in-process test**

In `internal/ingest/ingest_test.go`, add a comment above `TestConcurrentReadDuringWrite`
(line ~188): `// NOTE: in-process only; cross-process safety is covered by TestCrossProcessConcurrentIngest.`
Keep the test.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/clio-ingest-once internal/ingest/concurrent_subprocess_test.go internal/ingest/ingest_test.go
git commit -m "test(ingest): cross-process concurrency test (no dupes/drift/reversion)"
```

---

## Final verification

- [ ] **Run the whole suite + vet + build:**

```bash
go vet ./...
go test ./...
go build ./...
```
Expected: all PASS.

- [ ] **Failover smoke (optional, manual):** start two `clio mcp`, kill the first,
  confirm the second logs `lease superseded`/promotion and keeps indexing (append
  to a session file, then query via the second).

---

## Self-review notes (spec coverage)

- §4.1 fenced lease → Task 7. §4.2 roles/two-handles → Tasks 8–9. §4.3 best-effort
  read catch-up → Task 9 (`beforeRead`, throttled). §4.4.1 IMMEDIATE → Task 1.
  §4.4.2 idempotent messages → Task 3. §4.4.3 in-txn re-validation → Task 6.
  §4.4.4 monotonic watermark → Task 5. §4.4.5 turn_count → Task 4. §4.6 fenced
  migrations → Task 2. §4.7 migration 0003 → Task 2. §7 subprocess test → Task 11.
  §9 common.go alignment → Task 10.
- Intervals match spec §8: TTL 10s (`DefaultTTL`), renew 3s, promotion poll 5s.
