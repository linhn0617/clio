## Context

Redaction lives in `internal/ingest/redact.go` (a list of `regexp` rules + `Redact(string) string`) and is applied in `internal/ingest/parser.go`:
- `raw := Redact(string(line))` (whole serialized line → `raw_json`)
- `add(role, Redact(content), ...)` (per-message FTS/`content`)
- `TitleHint = titleFrom(asString)` / `titleFrom(b.Text)` — **not** redacted; flows to `sessions.title`.

The regex-on-serialized-text approach misses secrets under generic JSON keys
(`apiKey`, `token`, `DATABASE_URL`) and connection strings, and the title path skips
redaction entirely.

## Decision

**Structured redaction over parsed JSON, plus regex on free text.**

### 1. New `redactJSON(line []byte) []byte`
- `json.Unmarshal(line, &any)`. If unmarshal fails (not valid JSON), fall back to
  `[]byte(Redact(string(line)))` (current behavior) so malformed lines are still scrubbed.
- Recursively walk `map[string]any` / `[]any`:
  - For a `map` entry `k: v` where `v` is a string and `isSecretKey(k)` is true →
    replace with `[REDACTED:key]`.
  - For any other string value → replace with `Redact(value)` (shape regexes).
  - Recurse into nested maps/slices.
- `json.Marshal` the result → redacted `raw_json`.
- `isSecretKey(k)`: case-insensitive substring match against a curated set:
  `secret, password, passwd, token, apikey, api_key, accesskey, access_key,
  privatekey, private_key, credential, auth, dsn, connectionstring, conn_str`.
  (Keep the set tight to avoid over-redacting benign keys; `url` is intentionally
  NOT key-matched — connection strings are caught by the value regex instead, so a
  plain `"url":"https://example.com"` is preserved.)

### 2. New connection-string regex in `redactRules`
`(?i)\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s:/@]+@\S+` → `[REDACTED:connstring]`.
Matches `postgres://user:pass@host/db`, `redis://h:p@…`, etc. Requires a `user:pass@`
authority so it does not touch credential-less URLs.

### 3. Title from redacted text
`titleFrom` is called on the parsed text; change the call sites so the title is
derived from `Redact(text)` (free text → shape regexes). Equivalent: derive the
title from the already-redacted `m.Content`. The title is short (first line, ~100
runes) so regex cost is negligible.

### 4. Wiring in `parser.go`
- `raw := redactJSON(line)` (was `Redact(string(line))`).
- `TitleHint` derived from redacted text.
- `add(...)` unchanged (already redacts `content`).

## Trade-offs

- **`raw_json` re-marshalling** loses byte-for-byte fidelity (key order / whitespace
  may change; numbers re-serialized). `raw_json` is surfaced only by `clio show
  --format raw` for human reading, not byte-exact replay, so this is acceptable. The
  malformed-JSON fallback preserves the old text-regex behavior for non-JSON lines.
- **Key-name set is curated, not exhaustive.** Shape regexes (incl. the new
  connstring rule) are the safety net for values under unknown keys. AWS bare secret
  values (no prefix) remain best-effort — documented, not silently implied complete.

## Risks / mitigations

- Over-redaction of benign data: mitigated by a tight key set and by only
  key-redacting string values (not numbers/bools).
- Performance: one extra unmarshal+marshal per line; ingest is not latency-critical
  and lines are bounded. Acceptable.
