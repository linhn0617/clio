## 1. Connection-string rule + isSecretKey (TDD)

- [x] 1.1 Failing tests in `redact_test.go`: `Redact` redacts `postgres://user:pass@host/db`
  and `redis://h:p@x` but leaves `https://example.com` and `https://user@host` (no
  password) untouched.
- [x] 1.2 Add the connection-string rule to `redactRules`; tests green.
- [x] 1.3 Failing tests for `isSecretKey`: TRUE for `apiKey`,`API_KEY`,`token`,
  `db_password`,`access_key`,`secret_key`,`auth_token`,`credentials`,`dsn`;
  FALSE for `url`,`name`,`id`,`title`,`author`,`tokenizer`,`oauth_provider`,`publicKey`.
- [x] 1.4 Implement `isSecretKey` as a lowercase word-boundary regex (NOT substring); tests green.

## 2. redactString + redactJSON (TDD)

- [x] 2.1 Failing tests for `redactString`: plain text with `sk-…20+` redacted by regex;
  a JSON-as-text value `{"apiKey":"secret-value-123456"}` → key value `[REDACTED:key]`;
  plain prose unchanged; CJK text unchanged.
- [x] 2.2 Failing tests for `redactJSON([]byte) []byte`:
  - `{"apiKey":"x123456","note":"hi","nested":{"token":"abc123456"}}` → both secret
    values `[REDACTED:key]`, `note` preserved.
  - whole-subtree: `{"token":123}` and `{"auth_token":{"u":"a","p":"b"}}` and
    `{"credential":["x","y"]}` → value becomes `[REDACTED:key]` (number/object/array).
  - benign key carrying a shape secret: `{"msg":"sk-aaaaaaaaaaaaaaaaaaaa"}` →
    redacted by regex; `{"url":"https://example.com"}` preserved.
  - `<`,`>`,`&` in a value are PRESERVED (not `<`); a large integer
    (`{"id":123456789012345678}`) is preserved exactly (UseNumber).
  - non-JSON line → falls back to text `Redact` (no panic, still scrubbed).
  - deep nesting + arrays of objects redact at every level; CJK values intact.
- [x] 2.3 Implement `redactString` (regex + JSON-aware recurse) and `redactJSON`
  (UseNumber decode, whole-subtree redaction on secret keys, `SetEscapeHTML(false)`
  encode, trailing-newline trim, fallback to `Redact` on decode error); tests green.

## 3. Wire into the parser (TDD)

- [x] 3.1 Failing ingest regression test in `ingest_test.go`: ingest a session whose
  FIRST user message is `OPENAI_API_KEY=sk-aaaaaaaaaaaaaaaaaaaa`, whose second user
  message is the text `{"apiKey":"secret-value-123456"}`, and whose assistant event's
  raw JSON contains `{"token":"plainsecret123"}`; assert `sessions.title`,
  every `messages.content`, and every `messages.raw_json` for that session contain
  NO raw secret (only `[REDACTED:*]`).
- [x] 3.2 In `parser.go`: `raw := string(redactJSON(line))`; `content` via
  `redactString`; `TitleHint` via `redactString(text)` at both call sites
  (`asString` and `b.Text`). Run 3.1 green.
- [x] 3.3 Existing redaction/parser/ingest tests still pass (adjust any that asserted
  on exact raw_json bytes to tolerate normalized JSON).

## 4. Verify

- [x] 4.1 `go test ./internal/ingest/ -race -count=1` green.
- [x] 4.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l` empty.
- [x] 4.3 Self-review, then codex re-review of the diff; address findings. (5 codex
  passes: plan-review + 4 impl rounds; final verdict "no findings, ship as-is".)
