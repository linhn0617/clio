## 1. B3 persisted activity backfill watermark

- [x] 1.1 Tests first: unchanged read-path upgrade, unfillable row, failure retry,
      new messages, split-event alignment, no-write no-op, replacement IDs, query plan
- [x] 1.2 Migration 0016 creates unseeded singleton state; migration test
- [x] 1.3 Range-check new IDs; advance progress atomically with targets, including unfillable rows
- [x] 1.4 Always backfill in runCatchUp and runIndexIngest; preserve event block alignment

## 2. B12 cheap wins

- [x] 2.1 Migration 0015 + test (index gone, session_uuid plans use `idx_messages_session_seq`)
- [x] 2.2 256 KB read buffer in the Claude, Codex, and usage streaming readers
- [x] 2.3 `// debt:` markers for the deferred commit batching and incremental usage read

## 3. Verification

- [x] 3.1 Race tests, negative controls, `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `openspec validate --all --strict`
