## 1. LIKE escaping helper (TDD)

- [ ] 1.1 Failing test in `internal/db/db_test.go` for `EscapeLike`: `EscapeLike("a%_\\b")` == `a\%\_\\b`; plain text unchanged.
- [ ] 1.2 Implement `db.EscapeLike`; green.

## 2. Literal-safe MATCH builder (TDD)

- [ ] 2.1 Failing test in `internal/search/query_test.go` for `buildMatchQuery`:
  `["c++","foo"]` → `"c++" "foo"`; a term containing `"` is doubled; empty slice → "".
- [ ] 2.2 Implement `buildMatchQuery`; green.

## 3. Hybrid planner + escaping (TDD, integration)

- [ ] 3.1 Failing tests in `internal/search/search_test.go` (on-disk DB via existing
  helpers): seed messages; assert:
  - query `c++` (and `"unclosed`, `foo OR`, `(test`) returns WITHOUT error (no fts5
    syntax error) and matches literal content.
  - mixed-length `auth ui` matches a message containing both "auth" and "ui" and does
    NOT error; a message with only "auth" is excluded.
  - a `--project` prefix containing `_` (e.g. `/x/a_b`) matches only that project, not
    `/x/axb` (escaping works).
- [ ] 3.2 Implement: `buildMatchQuery`-based `ftsQuery` over the long terms + per-short-term
  `AND m.content LIKE ? ESCAPE '\'`; all-short → escaped `likeQuery`; remove
  `needsLikeFallback`; escape content + project LIKE via `db.EscapeLike`. Green.
- [ ] 3.3 Add graceful MATCH→LIKE fallback in `Search` on an fts error; test by ensuring
  no realistic input errors (the builder covers it) — keep the fallback as insurance.

## 4. sessions.ListSessions project escaping + index

- [ ] 4.1 Escape the `project_path LIKE` in `internal/sessions/sessions.go` `ListSessions`
  with `db.EscapeLike` + `ESCAPE '\'`; add/adjust a test asserting `_`/`%` don't over-match.
- [ ] 4.2 Add `internal/db/migrations/0004_search_indexes.sql` (`idx_sessions_ended`,
  `idx_messages_role_ts`); confirm migrations still apply (db tests green).

## 5. Verify

- [ ] 5.1 `go test ./internal/search/ ./internal/sessions/ ./internal/db/ -race -count=1` green.
- [ ] 5.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l .` empty.
- [ ] 5.3 Self-review, then codex re-review of the diff; address findings.
