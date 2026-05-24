## 1. ResolvePrefix exact-match-first (TDD)

- [ ] 1.1 Failing test in `internal/sessions/sessions_test.go`: seed sessions whose uuids
  share a prefix where one uuid is itself an exact value AND a prefix of others (e.g.
  `abc`, `abcd`, `abcde`); `ResolvePrefix(d, "abc")` returns the exact `abc` (not
  `ErrAmbiguous`). Also: an unambiguous prefix resolves; an ambiguous prefix with no exact
  match returns `ErrAmbiguous`; an unknown prefix returns `ErrNotFound`; a prefix
  containing `_` (e.g. `a_x` vs `abx`) does not over-match.
- [ ] 1.2 Implement: exact `uuid = ?` via `QueryRow` first (return on hit; propagate
  non-`sql.ErrNoRows` errors); else escaped `uuid LIKE ? ESCAPE '\' LIMIT 2`; check
  `rows.Err()`. Add `database/sql` import. Green.

## 2. show --format raw dedup (TDD)

- [ ] 2.1 Failing test in `internal/cli/show_test.go` (new) or `internal/cli/common_test.go`:
  given a `[]sessions.Message` with two adjacent identical `RawJSON` values and one
  different, the raw writer emits the duplicate once and preserves a later distinct line.
  (Extract the raw loop into a helper `writeRaw(w io.Writer, msgs []sessions.Message)` to
  test without cobra/stdout.)
- [ ] 2.2 Implement `writeRaw` with adjacent dedup; call it from the `case "raw"` branch.
  Green.

## 3. claudeconfig reject non-object mcpServers (TDD)

- [ ] 3.1 Failing test in `internal/claudeconfig/claudeconfig_test.go`: write a config with
  `"mcpServers": []` (array) and one with `"mcpServers": "x"` (string); `AddServer` returns
  an error AND the on-disk file is byte-for-byte unchanged (no `.bak` left behind);
  `HasServer` returns an error. A normal object config still works (regression).
- [ ] 3.2 Implement: `serversMap` returns `(map[string]any, error)`; error on present
  non-object; update `AddServer`/`RemoveServer`/`HasServer` callers to propagate. Green.

## 4. doctor non-zero exit + no swallowed errors (TDD)

- [ ] 4.1 Failing test in `internal/cli/doctor_test.go` (new): `reportDoctor(io.Discard,
  results)` returns non-nil when any `Result.OK` is false and nil when all OK.
- [ ] 4.2 Implement `reportDoctor(w io.Writer, results []doctor.Result) error` + sentinel
  `errChecksFailed`; `RunE` calls `return reportDoctor(os.Stdout, doctor.Run(...))`. Green.
- [ ] 4.3 Failing test in `internal/doctor/doctor_test.go`: open a DB, `DROP TABLE messages`,
  run `Run`; assert the `fts index` check is `OK == false` (a swallowed `Scan` error would
  leave it passing with count 0). 
- [ ] 4.4 Implement: capture `Scan` errors on the `messages` count, orphan count, ingest
  `tracked` count, and `sourceBytes` (now `(int64, error)`) → mark the affected check
  failed; add `rows.Err()` after the `reconcile` loop. Existing healthy-DB checks still
  pass (regression). Green.

## 5. activity_summary local-day grouping (TDD)

- [ ] 5.1 Failing test in `internal/sessions/sessions_test.go`: seed two sessions whose
  `ended_at` are 23:30 and 00:30 of consecutive **local** days (compute via
  `time.Date(..., time.Local)`); `ActivitySummary(d, since, "day")` returns two buckets
  whose keys equal `time.Unix(ts,0).Local().Format("2006-01-02")` for each. (A UTC grouping
  collapses them into one bucket under a non-UTC `TZ`.)
- [ ] 5.2 Implement: `keyExpr = "date(s.ended_at,'unixepoch','localtime')"` for the
  `day`/`""` case. Green. Existing `TestActivitySummaryGrouping` still passes.

## 6. Verify

- [ ] 6.1 `go test ./internal/sessions/ ./internal/cli/ ./internal/claudeconfig/ ./internal/doctor/ -race -count=1` green.
- [ ] 6.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l .` empty.
- [ ] 6.3 Self-review, then codex re-review of the diff; address findings.
