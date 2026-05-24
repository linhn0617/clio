# session-ingest Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: Scan and ingest Claude Code session files

The system SHALL scan `~/.claude/projects/**/*.jsonl` and ingest each session's events into SQLite.

#### Scenario: Full ingest of a project directory

- **WHEN** `clio index` runs against `~/.claude/projects/`
- **THEN** the system SHALL walk every `.jsonl` file, parse each line as an event, and UPSERT one row per session into `sessions` and one row per message into `messages`, populating the FTS index for searchable content

### Requirement: Project path derived from event cwd

The system SHALL determine a session's project path from the first event containing a `cwd` field, NOT by decoding the encoded directory name.

#### Scenario: Path with underscores and hyphens

- **WHEN** a session file lives under `-Users-lin-Herd-cli-project-COMPLETE` and its events contain `"cwd":"/Users/lin/Herd/cli_project_COMPLETE"`
- **THEN** the system SHALL store `project_path` as `/Users/lin/Herd/cli_project_COMPLETE`, using the directory name only as a fallback when no event carries `cwd`

### Requirement: Incremental append-aware ingest

The system SHALL re-ingest only newly appended content using a last-complete-newline
offset plus head and tail fingerprint checks, SHALL stream the unread tail with bounded
memory, and SHALL never silently drop a complete line it cannot parse.

#### Scenario: File grew by a partial line

- **WHEN** a `.jsonl` file has grown since last ingest and its tail ends mid-line
- **THEN** the system SHALL parse only up to the last complete newline, persist that
  offset as `last_byte_offset`, and leave the partial trailing bytes for the next run

#### Scenario: Same-size rewrite detected

- **WHEN** a file's size and mtime suggest no change but its tail fingerprint differs
  from the stored fingerprint
- **THEN** the system SHALL treat the file as changed and re-ingest

#### Scenario: Pre-watermark rewrite detected by head fingerprint

- **WHEN** a file is rewritten so that its leading bytes differ from the stored head
  fingerprint, even if the tail bytes at the stored offset are unchanged
- **THEN** the system SHALL fall back to a full reingest rather than resuming an append

#### Scenario: Large unread tail is read with bounded memory

- **WHEN** the unread tail between the watermark and EOF is large
- **THEN** the system SHALL stream complete lines without loading the entire tail into
  memory at once, while preserving single-transaction-per-file commit

#### Scenario: Complete line that cannot be parsed

- **WHEN** ingest reads a complete (newline-terminated) line that fails to parse
- **THEN** the system SHALL skip that line, increment a persistent per-source
  `unparsed_lines` counter, and still advance the watermark so the failure does not
  block later lines in the same file

### Requirement: Secret redaction during ingest

The system SHALL redact secret patterns before persisting content, covering both
free-text values (via shape patterns, including `Authorization: Basic` and
`Cookie`/`Set-Cookie` headers) and structured JSON (via key-name matching, including
the `authorization` and `cookie` keys), and SHALL ensure no secret reaches the
session title.

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

### Requirement: Exclude clio's own MCP traffic

The system SHALL skip events that are clio's own MCP tool calls to prevent self-pollution.

#### Scenario: Indexing a session that queried clio

- **WHEN** ingest encounters a `tool_use` or `tool_result` whose server/tool name belongs to clio
- **THEN** the system SHALL NOT write that message into `messages` or the FTS index

### Requirement: Per-message size cap and atomic per-file ingest

The system SHALL cap FTS content per message and commit each file in a single
transaction, and SHALL abort a commit whose source snapshot can no longer be validated.

#### Scenario: Oversized tool output

- **WHEN** a single message's content exceeds 64KB
- **THEN** the system SHALL truncate the FTS-indexed content while preserving the full
  `raw_json`

#### Scenario: Crash mid-file

- **WHEN** ingest of a file is interrupted before commit
- **THEN** the system SHALL leave `ingest_state` unchanged so the next run re-ingests
  that file from its prior offset

#### Scenario: Source changed or unreadable during commit re-validation

- **WHEN** the source file's size or mtime changed since it was read, OR the file can no
  longer be stat'd (removed or replaced) at commit time
- **THEN** the system SHALL abort the commit without writing, leave `ingest_state`
  unchanged, and let a later pass re-ingest the fresh bytes

