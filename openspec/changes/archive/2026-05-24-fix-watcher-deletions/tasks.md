## 1. PurgeMissing reconciliation helper (TDD)

- [x] 1.1 Failing test in `internal/ingest/ingest_test.go`: ingest two session files,
  delete one from disk, call `ing.PurgeMissing(ctx)`; assert the deleted session's rows
  in `sessions`/`messages`/`ingest_state` are gone, its FTS entries return no search
  hits, `tool_calls` orphans are removed, and the surviving session is untouched.
- [x] 1.2 Failing test: a source file that still exists but returns a non-`NotExist`
  stat error (simulate via a stat seam) is NOT purged.
- [x] 1.3 Implement `PurgeMissing(ctx, projectsDir)` in `internal/ingest`: enumerate
  `source_file` from `ingest_state` ∪ `sessions`; for each, `os.Stat`; on `errors.Is(err,
  fs.ErrNotExist)` purge in one transaction (messages → tool_calls orphans → sessions →
  ingest_state); other errors skip. Respect `ctx` cancellation between files. Green.
- [x] 1.4 Failing tests for the blast-radius guards (eng-review D1 + outside-voice #3/#6):
  (a) when `projectsDir` is missing/unreadable (test via `os.ReadDir` error), `PurgeMissing`
  purges NOTHING and returns nil; (b) a small history where both sources are deleted (2/2)
  STILL purges (ratio alone must not block small installs); (c) a large history where the
  missing set is both a large count (>10) AND >50% aborts the purge, logs a warning, leaves
  rows. Implement the root guard via `os.ReadDir` and the combined count-AND-ratio cap.
  Green.

## 2. Wire into backstop + CLI catch-up (TDD)

- [x] 2.1 Failing test in `internal/watcher/watcher_test.go`: after a backstop tick with
  a deleted source, the session is purged from the DB.
- [x] 2.2 Call `PurgeMissing` in the watcher backstop branch (alongside `IngestAll`) and
  in the CLI catch-up path. Green; existing watcher/CLI tests still pass.

## 3. pidAlive treats EPERM as alive (C8, TDD)

- [x] 3.1 Failing test in `internal/lock/lock_test.go`: with the `signalProc` seam
  overridden to return `syscall.EPERM`, `pidAlive(pid)` returns true; returning `nil`
  → true; returning `os.ErrProcessDone`/ESRCH → false.
- [x] 3.2 Add the `signalProc` package seam; change `pidAlive` to return true on `nil`
  or `errors.Is(err, syscall.EPERM)`. Green; existing lock tests pass.

## 4. Verify

- [x] 4.1 `go test ./internal/ingest/ ./internal/watcher/ ./internal/lock/ ./internal/cli/ -race -count=1`
  green.
- [x] 4.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l` empty.
- [x] 4.3 Self-review, then codex adversarial re-review of the diff; address findings.
