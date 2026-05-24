## 1. Basic auth + cookie shape rules (TDD)

- [x] 1.1 Failing tests in `internal/ingest/redact_test.go`: `Redact` replaces the
  credential in `Authorization: Basic dXNlcjpwYXNzd29yZA==` (result contains
  `Basic [REDACTED:token]`, not the base64) and in `Cookie: session=abc123; csrf=xyz`
  and `Set-Cookie: session=abc123` (result contains `[REDACTED:cookie]`, not the
  value). Negative cases: the prose word "basic" and a sentence mentioning "cookie"
  (no header colon form) are left unchanged.
- [x] 1.2 Add the two rules to `redactRules` in `internal/ingest/redact.go`
  (`(?i)\bBasic\s+[A-Za-z0-9+/=]{8,}` and `(?i)\b(Set-)?Cookie:\s*[^\r\n]+`). Green.

## 2. authorization / cookie secret keys (TDD)

- [x] 2.1 Failing tests in `redact_test.go`: `isSecretKey` returns true for
  `authorization`, `Authorization`, `cookie`, `set-cookie`, `setCookie`; and
  `redactWalk` over `{"authorization":"Basic x"}`, `{"cookie":"session=y"}`,
  `{"set-cookie":["a","b"]}` redacts the whole value to `[REDACTED:key]`. Negative:
  `author`, `oauth_provider`, `tokenizer` still return false (no regression).
- [x] 2.2 Add `authorization`, `cookie`, `set cookie` to `secretKeyRe` in
  `internal/ingest/redact.go`. Green.

## 3. Ingest regression (TDD)

- [x] 3.1 Failing ingest regression in `internal/ingest/ingest_test.go`: ingest a
  session whose messages contain `Authorization: Basic dXNlcjpwYXNzd29yZA==`, a
  `Cookie: session=topsecret123` line, and a raw event with
  `{"authorization":"Basic dXNlcjpwYXNz"}`; assert no `messages.content`,
  `messages.raw_json`, or `sessions.title` for that session contains the raw
  base64 / cookie value.
- [x] 3.2 Confirm green with the rules + keys from tasks 1-2 (no parser change needed;
  `redactString`/`redactJSON` already route through `Redact` and `redactWalk`).

## 4. Verify

- [x] 4.1 `go test ./internal/ingest/ -race -count=1` green.
- [x] 4.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l internal/ingest` empty.
- [x] 4.3 Self-review, then codex adversarial re-review of the diff; address findings.
