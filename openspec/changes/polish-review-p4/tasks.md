## 1. claudeconfig cross-process lock (C6, TDD)

- [ ] 1.1 Failing test in `internal/claudeconfig/claudeconfig_test.go`: two sequential
  `AddServer` calls for different server names against the same config both end up
  present (guards the read-modify-write); widen the window via the `renameFile` seam to
  make a lost update observable without the lock.
- [ ] 1.2 Wrap `mutate` in an OS file lock on `<configPath>.clio-lock` using the
  `internal/lock` flock seam; acquire before `load()`, release after rename. Serialization
  is Unix-only (mutex_other.go is a no-op); the `GOOS=windows` build must still pass.
  Green.

## 2. doctor permission checks (C9 verify, TDD)

- [ ] 2.1 Failing test in `internal/doctor/doctor_test.go`: a DB file chmod'd to `0644`
  produces a failing "permissions" check naming the mode; `0600` passes.
- [ ] 2.2 Add a `doctor` check stating db, `db.sqlite-wal`, `db.sqlite-shm`, config, and
  lock files and warning on any mode != `0600`. Green.

## 3. activity_summary group_by validation (R6, TDD)

- [ ] 3.1 Failing test in `internal/mcp` (handler test): `activity_summary` with
  `group_by:"week"` returns an MCP tool error mentioning the allowed values; `day`/
  `project` still work.
- [ ] 3.2 Validate `group_by` in `handleActivitySummary` before calling `sessions`. Green.

## 4. context threading (R1, internal)

- [ ] 4.1 Add `ctx context.Context` to `search.Search`, the `sessions` query funcs, and
  the `db` query helpers; use `QueryContext`/`ExecContext`/`QueryRowContext`.
- [ ] 4.2 Pass the handler `ctx` from `internal/mcp` tool handlers and `cmd.Context()`
  from CLI commands. Build green; all existing tests green (no behavior change).
- [ ] 4.3 Add a cancellation test: a cancelled `ctx` makes a query return promptly with
  a context error.
- [ ] 4.4 Guardrail (eng-review): verify background ingest paths (watcher loop, backstop,
  `PurgeMissing`, leader catch-up) use a long-lived ctx, not a per-request handler ctx.
  Add/keep a test that a completed MCP tool call does not cancel in-flight background
  ingest. Reads use the handler ctx; background writes use the lifecycle ctx.

## 5. pragmas + sidecar perms (R4, C9, internal)

- [ ] 5.1 Add `_pragma=cache_size(-N)` to the DSN and run `ANALYZE` once after migrations
  in `db.Open`. Existing db tests green.
- [ ] 5.2 After open, `chmod 0600` on `path+"-wal"`/`path+"-shm"` when present. Covered
  by the doctor perm test (task 2).

## 6. search overscan (R3, internal)

- [ ] 6.1 Raise `search.overscan` (5 → 12) with a comment; existing ranking tests green.
  Add a test where a recency-boosted hit just outside the old bm25 top-N now survives
  re-ranking.

## 7. AddServer json error check (R6, internal)

- [ ] 7.1 Check the `json.Marshal`/`json.Unmarshal` errors in `AddServer` and return
  them wrapped instead of `_`. Existing claudeconfig tests green.

## 8. prepared statements — measure first (R2)

- [ ] 8.1 Add a Go benchmark for a from-scratch index of a realistic synthetic history
  in `internal/ingest`.
- [ ] 8.2 If the benchmark shows a meaningful win, convert the per-row `tx.Exec` inserts
  in `commit()` to prepared statements / multi-row inserts and re-run the benchmark to
  confirm. If not, record the measurement here and DROP the optimization (no unrequested
  change).

## 9. Verify

- [ ] 9.1 `go test ./... -race -count=1` green.
- [ ] 9.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l` empty.
- [ ] 9.3 Self-review, then codex adversarial re-review of the diff; address findings.
