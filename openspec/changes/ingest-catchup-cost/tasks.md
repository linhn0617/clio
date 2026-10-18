## 1. B3 backfill only when needed

- [x] 1.1 Tests first: read-path catch-up skips `BackfillActivity` on zero ingested files and calls
      it when files were ingested; `clio index` always calls it (`internal/cli/catchup_cost_test.go`)
- [x] 1.2 `runCatchUp` (common.go) and `runIndexIngest` (index.go)

## 2. B12 cheap wins

- [x] 2.1 Migration 0015 + test (index gone, session_uuid plans use `idx_messages_session_seq`)
- [x] 2.2 256 KB read buffer in the Claude and Codex streaming readers
- [x] 2.3 `// debt:` markers for the deferred commit batching and incremental usage read

## 3. Verification

- [x] 3.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `openspec validate --all --strict`
