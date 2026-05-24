## 1. ResolvePrefix exact-match-first (TDD)

- [x] 1.1 Failing test in `internal/sessions/sessions_test.go`: seed sessions whose uuids
  share a prefix where one uuid is itself an exact value AND a prefix of others (e.g.
  `abc`, `abcd`, `abcde`); `ResolvePrefix(d, "abc")` returns the exact `abc` (not
  `ErrAmbiguous`). Also: an unambiguous prefix resolves; an ambiguous prefix with no exact
  match returns `ErrAmbiguous`; an unknown prefix returns `ErrNotFound`; a full-uuid arg
  resolves; an empty prefix is handled (returns `ErrNotFound`/`ErrAmbiguous`, no panic);
  and prefixes containing `_`, `%`, and `\` (e.g. `a_x` vs `abx`, `a%x` vs `aXx`) do not
  over-match.
- [x] 1.2 Implement: exact `uuid = ?` via `QueryRow` first (return on hit; propagate
  non-`sql.ErrNoRows` errors); else escaped `uuid LIKE ? ESCAPE '\' LIMIT 2`; check
  `rows.Err()`. Add `database/sql` import. Green.

## 2. show --format raw dedup (TDD)

- [x] 2.1 Failing test in `internal/cli/show_test.go` (new): `writeRaw(w, msgs)` —
  (a) two adjacent messages with identical `RawJSON` emit that line once and a later
  distinct line still prints; (b) **guard**: two adjacent messages with *different*
  `RawJSON` both print (no over-collapse); (c) two identical lines separated by a distinct
  line print as three lines (adjacent-only, not global dedup).
- [x] 2.2 Implement `writeRaw(w io.Writer, msgs []sessions.Message) error` with
  adjacent-only dedup; `case "raw"` returns `writeRaw(os.Stdout, msgs)`. Green.

## 3. claudeconfig reject non-object mcpServers (TDD)

- [x] 3.1 Failing tests in `internal/claudeconfig/claudeconfig_test.go`:
  - `"mcpServers": []` (array) and `"mcpServers": "x"` (string): `AddServer` AND
    `RemoveServer` each return an error, the on-disk file is byte-for-byte unchanged, and
    no `.bak` is left behind.
  - `HasServer` returns an error for those.
  - `"mcpServers": null`: `AddServer` succeeds and writes `{"mcpServers":{"clio":…}}`
    (null treated as absent).
  - a normal object config still works (regression).
- [x] 3.2 Implement: `serversMap` returns `(map[string]any, error)`; `nil`/absent → empty
  map; present non-null non-object → error; update `AddServer`/`RemoveServer`/`HasServer`
  callers to propagate (error inside the `mutate` closure returns before any write). Green.

## 4. doctor non-zero exit + no swallowed errors (TDD)

- [x] 4.1 Failing test in `internal/cli/doctor_test.go` (new): `reportDoctor(io.Discard,
  results)` returns non-nil when any `Result.OK` is false and nil when all OK.
- [x] 4.2 Implement `reportDoctor(w io.Writer, results []doctor.Result) error` + sentinel
  `errChecksFailed`; `RunE` calls `return reportDoctor(os.Stdout, doctor.Run(...))`. Green.
- [x] 4.3 Failing tests in `internal/doctor/doctor_test.go`:
  - `DROP TABLE messages`, run `Run`; the `fts index` check is `OK == false` (a swallowed
    `Scan` error would leave it passing with count 0).
  - `DROP TABLE ingest_state`, run `Run`; the `source reconciliation` check is
    `OK == false` (Query failure must not read as green).
- [x] 4.4 Implement: capture `Scan` errors on the `messages` count, orphan count, ingest
  `tracked` count, and `sourceBytes` (now `(int64, error)`) → mark the affected check
  failed; change `reconcile` to `(missing, truncated, lag int, err error)` returning the
  error on `Query`/`rows.Scan`/`rows.Err`; `source reconciliation` fails when `err != nil`.
  Update the existing `reconcile` tests to the 4-value signature. Healthy-DB checks still
  pass (regression). Green.

## 5. activity_summary local-day grouping (TDD)

- [x] 5.1 Failing test in `internal/sessions/sessions_test.go`
  (`TestActivitySummaryLocalDay`): re-exec the test in a child process with
  `TZ=Asia/Taipei` (guarded by an env var). In the child, seed one session with
  `ended_at = 2026-05-01 20:00:00 UTC` (= 2026-05-02 in Taipei) plus a message; assert
  `ActivitySummary(d, ts-1, "day")` returns a single bucket whose key equals
  `time.Unix(ts,0).Local().Format("2006-01-02")` (`2026-05-02`). Pre-fix UTC grouping
  yields `2026-05-01` → red.
- [x] 5.2 Implement: `keyExpr = "date(s.ended_at,'unixepoch','localtime')"` for the
  `day`/`""` case. Green. Existing `TestActivitySummaryGrouping` still passes.

## 6. Verify

- [x] 6.1 `go test ./internal/sessions/ ./internal/cli/ ./internal/claudeconfig/ ./internal/doctor/ -race -count=1` green.
- [x] 6.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l .` empty.
- [ ] 6.3 Self-review, then codex re-review of the diff; address findings.
