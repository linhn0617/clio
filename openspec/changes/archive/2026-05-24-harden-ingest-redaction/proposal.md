## Why

A full-codebase review (internal + codex third-party) found that clio's secret
redaction has real gaps that break the privacy guarantee in the README:

1. **Session titles leak secrets.** `sessions.title` is derived from the first user
   text *before* redaction (`parser.go` `TitleHint`), so a first prompt like
   `OPENAI_API_KEY=sk-...` is shown verbatim by `clio list`, even though the message
   content itself is redacted.
2. **Structured secrets in `raw_json` slip through.** Redaction runs regexes over the
   serialized line. JSON like `{"apiKey":"internal-secret-123456"}` or a
   `postgres://user:pass@host/db` connection string is stored unredacted unless it
   happens to match a shape-specific token regex.

Redaction is a stated feature; these are trust bugs, not polish.

## What Changes

- **Modified** redaction to a structured pipeline: parse the event JSON, recursively
  redact string values whose key name is suspicious, AND run the existing shape
  regexes on every string value, then re-marshal to produce `raw_json`.
- **Added** a connection-string redaction rule (`scheme://user:pass@host`).
- **Modified** session-title derivation to use already-redacted text, closing the
  title leak.
- **Added** a regression guarantee: after ingesting a secret-bearing session, none of
  `sessions.title`, `messages.content`, or `messages.raw_json` contains the raw secret.

## Capabilities

### Modified Capabilities

- `session-ingest`: the "Secret redaction during ingest" requirement is strengthened
  to cover structured JSON keys, connection strings, and session titles.
