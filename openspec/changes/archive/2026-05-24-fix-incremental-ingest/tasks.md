## 1. Migration 0005 (TDD)

- [x] 1.1 Failing test in `internal/db/db_test.go`: after `Open`, `ingest_state` has
  columns `head_fingerprint` and `unparsed_lines` (query `PRAGMA table_info`).
- [x] 1.2 Add `internal/db/migrations/0005_ingest_state_cols.sql`:
  `ALTER TABLE ingest_state ADD COLUMN head_fingerprint TEXT NOT NULL DEFAULT '';`
  and `ALTER TABLE ingest_state ADD COLUMN unparsed_lines INTEGER NOT NULL DEFAULT 0;`.
  Green; migration idempotency test still passes.

## 2. Head + tail fingerprint rewrite detection (C2, TDD)

- [x] 2.1 Failing test in `internal/ingest/incremental_test.go` (or ingest_test): a
  file whose head bytes are rewritten (different first line) but tail-at-offset is
  unchanged is classified for full reingest, not incremental append.
- [x] 2.2 Add `headFingerprint` (first 512 bytes) to `FileState`; compute and persist
  it in `commit()`; in `IngestFile` resume, require BOTH head and tail fingerprints to
  match before `changeIncremental`, else `changeFull`. Green; existing same-size and
  partial-line scenarios still pass.
- [x] 2.3 Failing test (eng-review D2): a row with stored `head_fingerprint = ''`
  (pre-0005) resumes incrementally WITHOUT a full reingest and backfills the real head
  fingerprint on that pass. Implement the empty-as-unknown skip+backfill. Green.

## 3. Streaming bounded read (C4, TDD)

- [x] 3.1 Failing/seam test in `ingest_test.go`: ingest a file with many lines via the
  streaming path produces identical rows to the prior behavior (golden compare), and a
  trailing partial line (no newline) is left uningested with the watermark at the last
  complete newline.
- [x] 3.2 Replace `readFrom`/`io.ReadAll`/`splitLines` slurp with a `bufio.Reader`
  `ReadBytes('\n')` loop seeded at `startOffset`, feeding `parser.ParseLine` per line
  inside the existing single transaction; track byte offset for the watermark; stop at
  the last complete newline. Green; memory no longer scales with tail size.
- [x] 3.3 Failing tests (eng-review D3 + outside-voice #1): (a) a single line exceeding
  `maxLineBytes` (small test cap) that HAS a terminating newline is counted in
  `unparsed_lines`, discarded, the reader advances past its newline, and following valid
  lines still ingest; (b) two-pass: a giant line with NO newline yet does NOT advance the
  watermark on pass 1 (left as in-progress), and once the newline arrives on pass 2 it is
  discarded+counted+advanced. Implement the per-line cap with newline-confirmed discard.
  Green.

## 4. Unparseable-line counter + advance (C3, TDD)

- [x] 4.1 Failing tests in `ingest_test.go` (outside-voice #2 semantics): (a) a file with
  one valid line, one complete-but-unparseable line, then another valid line ingests BOTH
  valid lines and sets `ingest_state.unparsed_lines` to 1; (b) a SECOND incremental pass
  that appends only clean lines leaves the counter at 1 (ACCUMULATE, not overwrite to 0);
  (c) a `changeFull` reingest resets the counter to the count seen in that full pass.
- [x] 4.2 In the streaming loop, count `ParseLine` errors per pass; in `commit()` ADD the
  pass count to `ingest_state.unparsed_lines` on incremental, and SET it to the pass count
  on `changeFull`; keep advancing the watermark. Green.

## 5. Commit stat-error abort (C1, TDD)

- [x] 5.1 Failing test in `ingest_test.go` using `preCommitHook` to REMOVE the source
  file just before commit re-validation: `commit()` returns `errStaleSnapshot` (no rows
  written, watermark unchanged), and a later pass re-ingests once the file is back.
- [x] 5.2 In `commit()`, change the `os.Stat` guard so `statErr != nil` returns
  `errStaleSnapshot` instead of falling through. Green; existing stale-snapshot test
  (size/mtime change) still passes.

## 6. Doctor reports unparseable lines (TDD)

- [x] 6.1 Failing test in `internal/doctor/doctor_test.go`: with `unparsed_lines > 0`
  in `ingest_state`, `doctor` adds a non-zero warning check naming the count and
  suggesting `clio index --full`; zero → passing/clean.
- [x] 6.2 Add the check to `internal/doctor/doctor.go` (sum `unparsed_lines`). Green.

## 7. Verify

- [x] 7.1 `go test ./internal/ingest/ ./internal/db/ ./internal/doctor/ -race -count=1`
  green.
- [x] 7.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l` empty.
- [x] 7.3 Self-review, then codex adversarial re-review of the diff; address findings.
