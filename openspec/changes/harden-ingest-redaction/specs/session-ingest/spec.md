## MODIFIED Requirements

### Requirement: Secret redaction during ingest

The system SHALL redact secret patterns before persisting content, covering both
free-text values (via shape patterns) and structured JSON (via key-name matching),
and SHALL ensure no secret reaches the session title.

#### Scenario: Shape-pattern secret in free text

- **WHEN** a message body contains a recognizable secret (e.g. `sk-…`, a JWT, an AWS
  access key id, a `Bearer <token>`, or a `KEY=value` env line)
- **THEN** the system SHALL replace it with a `[REDACTED:<type>]` marker in both the
  searchable `content` and the stored `raw_json`

#### Scenario: Connection string with embedded credentials

- **WHEN** content contains a credentialed connection string such as
  `postgres://user:pass@host/db`
- **THEN** the system SHALL replace it with `[REDACTED:connstring]`, while leaving
  credential-less URLs (e.g. `https://example.com`) untouched

#### Scenario: Secret under a structured JSON key

- **WHEN** a session event's JSON contains a string value under a secret-bearing key
  (e.g. `{"apiKey":"…"}`, `{"token":"…"}`, `{"db_password":"…"}`)
- **THEN** the system SHALL replace that value with `[REDACTED:key]` in the stored
  `raw_json`, regardless of whether the value matches a shape pattern

#### Scenario: Secret embedded as JSON inside a text field

- **WHEN** a message body is (or contains) JSON text such as `{"apiKey":"…"}`
- **THEN** the system SHALL parse that JSON, replace the secret-keyed value with
  `[REDACTED:key]`, and store the redacted form in `content` and `raw_json`

#### Scenario: Whole value redacted regardless of type under a secret key

- **WHEN** a secret-bearing key holds a non-string value (number, array, or object)
  such as `{"auth_token":{"u":"a"}}`
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
