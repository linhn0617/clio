## 1. B3 persisted activity backfill watermark

- [x] 1.1 Tests first: unchanged read-path upgrade, unfillable row, failure retry,
      new messages, split-event alignment, no-write no-op, replacement IDs, query plan
- [x] 1.2 Migration 0016 creates unseeded singleton state; migration test
- [x] 1.3 Range-check new IDs; advance progress atomically with targets, including unfillable rows
- [x] 1.4 Keep read backfill watermark-based; runIndexIngest repairs all history in both modes
- [x] 1.5 Share pending predicate with both prune-raw guards; protect unexamined raw_json
- [x] 1.6 Best-effort, zero-busy-timeout no-pending advance; no Warn on contention
- [x] 1.7 Bounded predecessor queries for split-event alignment; bind RoleToolUse
- [x] 1.8 Share CLI ingestion/backfill flow; remove indistinguishable fake stats cases
- [x] 1.9 Restore modulo/IMMEDIATE explanations and correct stale CLI comments

## 2. B12 cheap wins

- [x] 2.1 Migration 0015 + test (index gone, session_uuid plans use `idx_messages_session_seq`)
- [x] 2.2 256 KB message-reader buffers; retain a separate 512 KB full-file usage buffer
- [x] 2.3 `// debt:` markers for the deferred commit batching and incremental usage read

## 3. Verification

- [x] 3.1 Race tests, negative controls, `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `openspec validate --all --strict`

## 4. PR repair verification

- [x] 4.1 Tests first: below-watermark read skip / both index repair modes
- [x] 4.2 Tests first: examined unfillable rows allow prune; unexamined rows stay protected
- [x] 4.3 Tests first: second connection holds write transaction; read backfill returns promptly without Warn
- [x] 4.4 Temporary negative controls, full acceptance checks, and sandbox-only failure report

Verification result: focused regressions pass; formatting, vet, and strict OpenSpec
validation pass. Full and race suites each fail only the unchanged
`internal/cli/recall_test.go:28` `TestProjectRoot`: checkout-local TMPDIR makes its
"no-git" directory inherit this worktree's `.git`. All other packages/tests pass;
no race is reported. The test and its assertions were not changed. Clean full-suite
acceptance remains unverified outside this filesystem restriction.
