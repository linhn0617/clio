## 1. Leader startup

- [x] 1.1 Tests first: lease renewed during a slow catch-up, supersede during catch-up cancels it
      and demotes, catch-up runs once, early read during catch-up neither blocks nor walks concurrently
- [x] 1.2 Remove the synchronous pre-Serve `IngestAll`; renewer goroutine started at the top of
      `leaderLoop` and awaited before return; `leaderEnv` + shared `ingestMu`; `newBeforeRead`
- [x] 1.3 stdio purity E2E indexes before starting the server (it relied on the removed synchronous ingest)

## 2. Specs

- [x] 2.1 file-watcher delta: single-writer startup order

## 3. Verification

- [x] 3.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `go test -race` on cli and lock
- [x] 3.2 `openspec validate --all --strict`
