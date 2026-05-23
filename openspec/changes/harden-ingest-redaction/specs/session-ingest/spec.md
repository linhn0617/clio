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

#### Scenario: Session title never contains a raw secret

- **WHEN** the first user message used to derive a session's title contains a secret
- **THEN** the stored `sessions.title` SHALL contain only the redacted form, never the
  raw secret
