## 1. Connection-string + key-name redaction primitives (TDD)

- [ ] 1.1 Write failing tests in `internal/ingest/redact_test.go`: `Redact` redacts
  `postgres://user:pass@host/db` (and `redis://h:p@x`) but leaves `https://example.com`
  and `https://user@host` (no password) untouched.
- [ ] 1.2 Add the connection-string rule to `redactRules` in `redact.go`; run tests green.
- [ ] 1.3 Write failing tests for `isSecretKey`: true for `apiKey`/`API_KEY`/`token`/
  `db_password`/`access_key`/`dsn`; false for `url`/`name`/`id`/`title`.
- [ ] 1.4 Implement `isSecretKey(string) bool` in `redact.go`; tests green.

## 2. Structured JSON redaction (TDD)

- [ ] 2.1 Write failing tests for `redactJSON([]byte) []byte`: input
  `{"apiKey":"internal-secret-123456","note":"hi","nested":{"token":"abcdef123456"}}`
  → both secret values become `[REDACTED:key]`, `note` preserved; a value matching a
  shape regex under a benign key (`{"msg":"sk-abc...20+"}`) is redacted by regex;
  a non-JSON line falls back to text `Redact` (no panic, still scrubbed).
- [ ] 2.2 Implement `redactJSON` (unmarshal→recurse→marshal, fallback to `Redact`
  on unmarshal error); tests green.

## 3. Wire into the parser (TDD)

- [ ] 3.1 Write a failing ingest-level regression test in `internal/ingest/ingest_test.go`:
  ingest a session whose FIRST user message is `OPENAI_API_KEY=sk-aaaaaaaaaaaaaaaaaaaa`
  and whose assistant event contains `{"apiKey":"secret-value-123456"}`; assert
  `sessions.title`, `messages.content`, and `messages.raw_json` for that session
  contain NO raw secret (only `[REDACTED:*]` markers).
- [ ] 3.2 In `parser.go`: change `raw := Redact(string(line))` to
  `raw := string(redactJSON(line))`; derive `TitleHint` from redacted text
  (`titleFrom(Redact(asString))` / `titleFrom(Redact(b.Text))`). Run 3.1 green.
- [ ] 3.3 Confirm existing redaction/parser tests still pass.

## 4. Verify

- [ ] 4.1 `go test ./internal/ingest/ -race -count=1` green.
- [ ] 4.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` all clean.
- [ ] 4.3 Self-review, then codex re-review of the diff; address findings.
