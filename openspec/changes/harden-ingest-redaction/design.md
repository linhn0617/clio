## Context

Redaction lives in `internal/ingest/redact.go` (a list of `regexp` rules + `Redact(string) string`) and is applied in `internal/ingest/parser.go`:
- `raw := Redact(string(line))` (whole serialized line → `raw_json`)
- `add(role, Redact(content), ...)` (per-message FTS/`content`)
- `TitleHint = titleFrom(asString)` / `titleFrom(b.Text)` — **not** redacted; flows to `sessions.title`.

The regex-on-serialized-text approach misses secrets under generic JSON keys
(`apiKey`, `token`, `DATABASE_URL`) and connection strings, and the title path skips
redaction entirely.

## Decision

**Structured redaction over parsed JSON, plus a JSON-aware string redactor used everywhere.**
(Revised after a codex plan review: the first draft left embedded-JSON-in-text leaking,
risked false positives on keys like `author`, and re-marshalled raw_json lossily.)

### 1. `redactString(s string) string` — the single text redactor
Used for message `content`, session `title`, and as the string-leaf handler inside
`redactJSON`. Steps:
1. Apply shape regexes (`Redact` rules incl. the new connstring rule).
2. If the (trimmed) string parses as a JSON **object or array**
   (`json.Unmarshal` succeeds and the top value is a container), recursively redact it
   via the same walker and re-emit it as a string. This closes the
   embedded-JSON-in-text leak (e.g. a user pasting `{"apiKey":"secret"}`).
   Plain text / scalars / parse failures → just the regex result.

### 2. `redactJSON(line []byte) []byte` — the event walker
- Decode with `json.Decoder` + `UseNumber()` (so large integers are not coerced
  through float64). On decode failure → fall back to `[]byte(Redact(string(line)))`.
- Walk `map[string]any` / `[]any`:
  - If `isSecretKey(k)` → replace the **entire value** (string, number, array, OR
    object subtree) with the string `"[REDACTED:key]"`. (Whole-subtree redaction
    covers `{"token":123}`, `{"auth_token":{...}}`, `{"credential":["x"]}`.)
  - Else if value is a string → `redactString(value)`.
  - Else recurse into nested maps/slices.
- Encode the result with a `json.Encoder` whose `SetEscapeHTML(false)` is set, so
  `<`, `>`, `&` in content are preserved (not turned into `<`). Strip the single
  trailing newline Encoder adds.

### 3. `isSecretKey(k string) bool` — word-boundary match, not substring
Lowercase `k`, then match a word-boundary regex (so `author`, `tokenizer`,
`oauth_provider` do NOT match):
`\b(password|passwd|secret|secrets|credential|credentials|token|apikey|api_key|accesskey|access_key|privatekey|private_key|secret_key|auth_token|dsn|connection_string|conn_str)\b`
- `url` is intentionally NOT key-matched (a plain `"url":"https://example.com"` is
  kept); credentialed URLs are caught by the connstring value regex instead.
- Bare `auth`/`conn` are intentionally excluded (false positives); only the compound
  forms above match.

### 4. Connection-string regex (in `redactRules`)
`(?i)\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s:/@]+@\S+` → `[REDACTED:connstring]`.
Requires a `user:pass@` authority, so credential-less URLs are untouched.

### 5. Wiring in `parser.go`
- `raw := string(redactJSON(line))` (was `Redact(string(line))`).
- `content` and `TitleHint` derived via `redactString(text)` (was `Redact`/none).
- `add(...)` uses `redactString` for `content`.

## Trade-offs

- **`raw_json` round-trip**: with `UseNumber()` + `SetEscapeHTML(false)`, numbers and
  `<>&` are preserved. Remaining non-fidelity: object key order and insignificant
  whitespace are normalized, and duplicate keys collapse to the last (Go decode
  semantics). `raw_json` is surfaced only by `clio show --format raw` for human
  reading, not byte-exact replay — accepted and documented in the spec.
- **Out of scope**: a secret used as a JSON *key name* (not value) is not redacted —
  unusual and documented.
- **Key set is curated, not exhaustive.** Shape regexes + connstring are the value-level
  safety net. AWS bare secret values (no distinctive prefix) remain best-effort.

## Risks / mitigations
- Over-redaction: mitigated by word-boundary key matching and the curated set.
- Performance: redactString attempts one `json.Unmarshal` per string value; non-JSON
  strings fail fast. Lines are bounded; ingest is not latency-critical. Acceptable.
