## ADDED Requirements

### Requirement: Claude Code harness elements are not indexed as user text

The ingester SHALL remove every complete `<system-reminder>`, `<task-notification>` and
`<agent-message …>` element, content included, from Claude Code user-role text before
storing `content` and deriving the session title. A user message or text block that is
empty after removal SHALL NOT be stored. `raw_json` SHALL NOT change. Assistant text,
tool inputs, tool results and Codex transcripts SHALL NOT be affected.

#### Scenario: Reminder appended to a prompt

- **WHEN** a user message is `please fix the walrus bug` followed by a blank line and a
  `<system-reminder>` element
- **THEN** the stored `content` and the session title SHALL be `please fix the walrus bug`,
  and `raw_json` SHALL still contain the reminder

#### Scenario: Harness-only message

- **WHEN** a user message, or one text block of it, is nothing but a `<task-notification>`,
  `<system-reminder>` or relayed `<agent-message>`
- **THEN** no message row SHALL be stored for it, it SHALL NOT become the session title,
  and its text SHALL NOT be searchable

#### Scenario: Previously indexed sessions

- **WHEN** a session was indexed by an older clio
- **THEN** its harness text SHALL stay in the index until `clio index --full`

### Requirement: Relayed agent messages lose their fixed frame lines

For an `<agent-message>` element, the ingester SHALL also remove the line
`Another Claude session sent a message:` directly before it and the paragraph beginning
`That "other Claude session" is an agent working inside this same session` after it
(blank lines between allowed), each only on an exact match of Claude Code's fixed text.

#### Scenario: Relayed subagent report

- **WHEN** a user message is the preamble line, an `<agent-message from="…">` element and
  the fixed trailer paragraph
- **THEN** nothing of it SHALL be stored

#### Scenario: Frame line on its own

- **WHEN** a user message contains `Another Claude session sent a message:` with no
  element after it
- **THEN** the line SHALL be kept

### Requirement: Harness elements are matched exactly and never swallow user text

An element SHALL be recognised only when its opening tag starts a line, its closing tag
ends a line (trailing whitespace allowed), and the lower-case tag name matches exactly.
Elements SHALL nest strictly: a closer SHALL close only the innermost open element. An
unclosed opener SHALL stay as literal text, SHALL NOT remove text after it, and SHALL NOT
shield a later complete element. Removal SHALL run in linear time.

#### Scenario: Tag mentioned in prose

- **WHEN** a user message contains `` `<task-notification>…</task-notification>` `` inside
  a sentence, or a line starting with `<system-reminder>` that is never closed
- **THEN** the message SHALL be stored unchanged

#### Scenario: Unclosed opener before a complete element

- **WHEN** an unclosed `<system-reminder>` line and user text are followed by a complete
  `<system-reminder>` element
- **THEN** the complete element SHALL be removed and the unclosed opener and the user text
  SHALL be kept

#### Scenario: Thousands of unclosed openers

- **WHEN** a user message holds 20,000 unclosed opening tags, or 10,000 nested elements
- **THEN** removal SHALL finish in linear time (well under 500 ms)

### Requirement: Harness removal runs after redaction

Harness element removal SHALL run after secret redaction and `<private>` removal, on the
same block-local text, so it only deletes already-redacted text and cannot expose
anything redaction or a `<private>` element hid.

#### Scenario: Private element across a harness closer

- **WHEN** a `<private>` element opens inside a `<system-reminder>` and closes after its
  closing tag
- **THEN** the private content SHALL NOT be stored

#### Scenario: Secret after a harness element

- **WHEN** a user message is a `<system-reminder>` element followed by a line with a token
- **THEN** the stored `content` SHALL hold the redaction marker and not the reminder
