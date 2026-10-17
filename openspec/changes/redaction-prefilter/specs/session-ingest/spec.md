## MODIFIED Requirements

### Requirement: Secret redaction during ingest

The system SHALL redact secret patterns before persisting content, covering both
free-text values (via shape patterns, including `Authorization: Basic` and
`Cookie`/`Set-Cookie` headers) and structured JSON (via key-name matching, including
the `authorization` and `cookie` keys), and SHALL ensure no secret reaches the
session title. Each redaction rule MAY be gated by a cheap necessary-condition check, but
the stored output SHALL be exactly what running every rule would produce.

#### Scenario: Shape-pattern secret in free text

- **WHEN** a message body contains a recognizable secret (e.g. `sk-…`, a JWT, an AWS
  access key id, a `Bearer <token>`, or a `KEY=value` env line)
- **THEN** the system SHALL replace it with a `[REDACTED:<type>]` marker in both the
  searchable `content` and the stored `raw_json`

#### Scenario: Basic auth header in free text

- **WHEN** content contains an `Authorization: Basic <base64>` header (or a bare
  `Basic <base64>` credential)
- **THEN** the system SHALL replace the credential with `Basic [REDACTED:token]`,
  leaving the prose word "basic" untouched

#### Scenario: Cookie header value

- **WHEN** content contains a `Cookie:` or `Set-Cookie:` header line
- **THEN** the system SHALL replace its value with `[REDACTED:cookie]`, leaving a
  sentence that merely mentions the word "cookie" untouched

#### Scenario: Connection string with embedded credentials

- **WHEN** content contains a credentialed connection string such as
  `postgres://user:pass@host/db`
- **THEN** the system SHALL replace it with `[REDACTED:connstring]`, while leaving
  credential-less URLs (e.g. `https://example.com`) untouched

#### Scenario: Secret under a structured JSON key

- **WHEN** a session event's JSON contains a string value under a secret-bearing key
  (e.g. `{"apiKey":"…"}`, `{"token":"…"}`, `{"db_password":"…"}`, `{"authorization":"…"}`,
  `{"cookie":"…"}`)
- **THEN** the system SHALL replace that value with `[REDACTED:key]` in the stored
  `raw_json`, regardless of whether the value matches a shape pattern

#### Scenario: Secret embedded as JSON inside a text field

- **WHEN** a message body is (or contains) JSON text such as `{"apiKey":"…"}`
- **THEN** the system SHALL parse that JSON, replace the secret-keyed value with
  `[REDACTED:key]`, and store the redacted form in `content` and `raw_json`

#### Scenario: Whole value redacted regardless of type under a secret key

- **WHEN** a secret-bearing key holds a non-string value (number, array, or object)
  such as `{"auth_token":{"u":"a"}}` or `{"set-cookie":["a","b"]}`
- **THEN** the system SHALL redact the entire value, not only string leaves

#### Scenario: Session title never contains a raw secret

- **WHEN** the first user message used to derive a session's title contains a secret
- **THEN** the stored `sessions.title` SHALL contain only the redacted form, never the
  raw secret

#### Scenario: raw_json fidelity is display-grade, not byte-exact

- **WHEN** redaction re-serializes an event's JSON for `raw_json`
- **THEN** the system SHALL preserve values including `<`, `>`, `&` and large
  integers, while object key order and insignificant whitespace MAY be normalized
  (raw_json is for display via `clio show --format raw`, not byte-exact replay)

#### Scenario: Prefiltering never changes the output

- **WHEN** a string is redacted with the per-rule necessary-condition prefilter
- **THEN** the result SHALL be byte-for-byte identical to running every redaction rule on it,
  for every input including text with markers from an earlier redaction, word-boundary edges,
  and rules that have no literal feature (which always run)

#### Scenario: Non-ASCII text keeps case-insensitive rules exact

- **WHEN** text contains a non-ASCII byte and a case-insensitive rule's feature contains `k` or
  `s` (Go folds U+212A to `k` and U+017F to `s`, e.g. `API_\u212aEY=supersecretvalue123`)
- **THEN** that rule SHALL run unconditionally, so the secret is redacted as without a prefilter

#### Scenario: A tool call's input is redacted once for its summary and target

- **WHEN** one tool call's input string feeds both its parameter summary and its activity target
- **THEN** the system SHALL redact that string once and use the same result for both, with
  output identical to redacting twice; passes that would otherwise change output (redaction is
  not idempotent on every input) SHALL NOT be dropped

