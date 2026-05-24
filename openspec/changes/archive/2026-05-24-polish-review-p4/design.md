# Design — polish-review-p4

Umbrella design: `docs/superpowers/specs/2026-05-24-clio-quality-security-batch-design.md` (change ④).

## Observable changes

### claudeconfig cross-process lock (C6)

`claudeconfig.mutate` is an unlocked read-modify-write. Serialize the whole
load→fn→atomic-write cycle with an OS file lock on a sibling lock path
(`~/.claude.json.clio-lock`), using the existing `internal/lock` flock seam
(`mutex_unix.go` / `mutex_other.go`). Acquire before `load()`, release after the rename.
Lock contention is rare (manual install/uninstall), so a blocking flock with the
existing behavior is fine. Corruption was never possible (atomic rename); this closes
the lost-update window.

Platform scope (outside-voice #5): `internal/lock`'s `mutex_other.go` is a no-op on
non-Unix, so the serialization guarantee holds on Unix only. The spec and tasks scope the
"serialized" claim to Unix; on Windows clio still relies on the atomic rename (no
corruption, but lost-update is possible). Concurrent `install-mcp` on Windows is rare;
real Windows file locking (`LockFileEx`) is deferred as out of scope. The `GOOS=windows`
build must still pass with the no-op lock.

### doctor permission checks (covers C9 sidecars)

Add a `doctor` check that stats the DB file and its `-wal`/`-shm` sidecars and warns for
any whose mode is not `0600`. This both surfaces drift and verifies the C9
sidecar-permission fix below. (Scoped to db+sidecars rather than also config/lock:
`config.*` paths resolve to the real user home, which would make the check stat the
developer's actual `~/.claude.json` during tests; db+sidecars are dbPath-derived and
test-controlled. Config and lock files are already created `0600` by their own packages.)

### group_by validation (R6)

In the `activity_summary` handler, reject any `group_by` other than `day`/`project`
with `mcp.NewToolResultError`, instead of relying on the downstream query to error.

## Internal changes (no spec delta)

- **context threading (R1):** add `ctx` params to the `search`, `sessions`, and `db`
  query helpers (use `QueryContext`/`ExecContext`/`QueryRowContext`); pass the handler
  `ctx` from `internal/mcp` and `cmd.Context()` from the CLI. Pure plumbing, no behavior
  change. HONESTY NOTE (outside-voice #4): this does NOT make a cancelled MCP request
  return promptly today, because handlers call `beforeRead()` (catch-up ingest) synchronously
  before the query, on the long-lived ctx. Threading ctx is idiom hygiene and groundwork; do
  NOT claim "enables cancellation/timeout". Making follower catch-up non-blocking/bounded is
  a separate follow-up, deferred out of this batch (it changes the MCP request path, not
  just plumbing). GUARDRAIL (eng-review): background ingest — the
  watcher loop, the 60s backstop, `PurgeMissing`, and the leader catch-up — MUST use a
  long-lived ctx (the server lifecycle ctx or `context.Background()`), NEVER a per-request
  MCP handler ctx, or a finished tool call would cancel in-flight background indexing.
  Reads triggered by a handler use the handler ctx; background writes do not.
- **pragmas (R4):** add `_pragma=cache_size(-8000)` to the DSN and run `PRAGMA optimize`
  after migrations in `db.Open` (chosen over an unconditional `ANALYZE`: it only
  re-analyzes tables that changed enough to matter, so it stays cheap on every open).
- **sidecar perms (C9):** after `db.Open`, `chmod 0600` on `path+"-wal"` and
  `path+"-shm"` if present; the data dir is already created `0700`.
- **overscan (R3):** raise `search.overscan` (e.g. 5 → 12) so a recency-boosted hit just
  outside the bm25 top-N is not dropped before re-ranking. Cheap; relevance only.
- **json.Marshal check (R6):** check the error from `json.Marshal`/`json.Unmarshal` in
  `AddServer` and return it wrapped, instead of `_`.
- **prepared statements (R2, measure first):** add a Go benchmark for a from-scratch
  index of a realistic synthetic history. Only if it shows a meaningful win, convert the
  per-row `tx.Exec` inserts in `commit()` to prepared statements / multi-row inserts;
  otherwise record "measured, not worth it" in the task and drop. No unrequested
  optimization.

## Sequencing within the change

Independent items; implement context threading first (widest diff, touches signatures
the other items live near), then the rest. Each is its own TDD step.

## Test strategy

- `claudeconfig`: a test that two serialized `AddServer` calls both land (no lost
  update); reuse the `renameFile` seam if needed to widen the race window.
- `doctor`: a test that a chmod-0644 db file produces a failing perm check.
- `mcp`: a test that `activity_summary` with `group_by:"week"` returns a tool error.
- context threading: existing tests must stay green; add one cancellation test where a
  cancelled `ctx` aborts a query.
- pragmas/sidecar/overscan/json-check: targeted unit tests; benchmark for R2.
