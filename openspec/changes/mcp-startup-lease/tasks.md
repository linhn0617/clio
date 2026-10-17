## 1. Leader startup

- [x] 1.1 Tests first: lease renewed during a slow catch-up, supersede during catch-up cancels it
      and demotes, catch-up runs once, early read during catch-up neither blocks nor walks concurrently
- [x] 1.2 Remove the synchronous pre-Serve `IngestAll`; renewer goroutine started at the top of
      `leaderLoop` and awaited before return; `leaderEnv` + shared `ingestMu`; `newBeforeRead`
- [x] 1.3 stdio purity E2E originally indexed first; superseded by 4.5 (it now polls search against the server's own startup catch-up)

## 2. Specs

- [x] 2.1 file-watcher delta: single-writer startup order

## 3. Verification

- [x] 3.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `go test -race` on cli and lock
- [x] 3.2 `openspec validate --all --strict`

## Review fixes

- [x] 4.1 Initial leader flag always false; leader takes the ingest mutex before Serve; flag published under it; read catch-up rechecks the flag after TryLock
- [x] 4.2 Shutdown cancels then joins catch-up, watcher and renewer before the lease is released and DBs close
- [x] 4.3 Watcher backstop walk runs under the shared mutex (`BackstopGuard`, skip when busy)
- [x] 4.4 Cancelled catch-up stops between phases and logs at info; cancel cause (`WithCancelCause`) replaces the superseded channel
- [x] 4.5 Command-level test asserts exactly one startup IngestAll; e2e polls search instead of pre-indexing
- [x] 4.6 Supersede tests rewrite the lease file under `lock.WithFileLock`; concurrency design doc updated

## Second review round

- [x] 5.1 Read catch-up walk bounded by the request ctx (mcp-go handler ctx) and the command ctx; cancellation not logged as failure; release via defer
- [x] 5.2 Superseded cancellation logged at warn (visible at the default log level)
- [x] 5.3 Shutdown-order test through RunE; test that newMCPRuntime wires the watcher BackstopGuard; e2e shutdown under a deadline
