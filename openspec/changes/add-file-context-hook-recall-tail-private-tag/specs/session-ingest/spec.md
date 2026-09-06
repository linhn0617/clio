## ADDED Requirements

### Requirement: User-marked private content is not indexed

The ingester SHALL remove every `<private>…</private>` element, including its
content, from user and assistant text before persisting, so that the text appears in
neither the searchable `content`, the stored `raw_json`, nor the derived session
title. Matching SHALL be case-insensitive on the tag name, SHALL tolerate attributes
and surrounding whitespace, SHALL treat nested elements as one span (an inner `<private>` never ends the
outer element early; two sibling blocks in one message remove exactly two spans), and
SHALL leave an unterminated `<private>` opening tag's text in
place (no silent loss of the rest of the message), and SHALL NOT treat a self-closing
`<private/>` as an opener. Removal SHALL apply to the same
text fields the secret redaction already walks, SHALL happen before redaction and before
any JSON-aware splitting of the text (a private block whose content is a JSON object or
array is removed whole), SHALL be block-local (an element that opens in one top-level content
block and closes in another is not recognised; the joined text of a tool_result's nested
blocks counts as one block for `content`, while `raw_json` keeps the halves), SHALL apply
to every source clio ingests (Claude Code, Codex, Gemini), and SHALL never modify the
source JSONL. Tool inputs and tool results SHALL be walked the
same way when they are strings or string leaves.

#### Scenario: Private block in a user prompt

- **WHEN** a user message contains `plan the migration <private>the customer is
  Acme, contract value 2M</private> by Friday`
- **THEN** the stored `content` and `raw_json` SHALL read `plan the migration  by
  Friday` (the element and its content gone) and a search for `Acme` SHALL not find
  the message

#### Scenario: Private block containing JSON

- **WHEN** a message contains `<private>{"customer":"Acme"}</private>`
- **THEN** the whole element SHALL be removed from `content` and `raw_json`, and `Acme`
  SHALL not be searchable

#### Scenario: Block spanning two content blocks

- **WHEN** `<private>` opens in one text block of an event and `</private>` sits in the
  next block
- **THEN** both blocks SHALL be stored unchanged apart from normal redaction (documented
  non-support)

#### Scenario: Block spanning a tool_result's nested blocks

- **WHEN** a tool_result's `content` array holds `head <private>alpha` and
  `beta</private> tail` as two text blocks
- **THEN** the stored `content` (the joined text) SHALL have the element removed and
  keep `head`/`tail`, and `raw_json` SHALL keep both halves (documented asymmetry of
  the unsupported form)

#### Scenario: Message that is only a private block

- **WHEN** a user message, or a tool input value, is nothing but
  `<private>secret</private>`
- **THEN** no message row (and no activity fact) SHALL be stored for it, so it never
  counts as a turn

#### Scenario: Applies to other sources

- **WHEN** a Codex transcript message contains `<private>sample</private>`
- **THEN** the stored `content` SHALL not contain `sample`

#### Scenario: Nested private blocks

- **WHEN** a message contains `<private>alpha <private>beta</private> gamma</private>`
- **THEN** the whole outer element SHALL be removed and neither `alpha`, `beta` nor
  `gamma` SHALL be stored

#### Scenario: Two blocks in one message

- **WHEN** a message contains two separate `<private>` elements
- **THEN** exactly those two spans SHALL be removed and the text between them kept

#### Scenario: Unterminated tag

- **WHEN** a message contains `<private>` with no closing tag
- **THEN** the message SHALL be stored unchanged apart from normal redaction

#### Scenario: Self-closing tag

- **WHEN** a message contains `x <private/> y <private>s</private> z`
- **THEN** only the second element SHALL be removed and `y` SHALL be kept

#### Scenario: Long private block in a tool input

- **WHEN** a tool call's `command` contains a `<private>` block longer than the
  200-byte summary cut
- **THEN** the stored tool summary SHALL contain none of the block's content

#### Scenario: Title never contains private content

- **WHEN** the first user message of a session opens with a `<private>` block
  followed by real text
- **THEN** the derived `sessions.title` SHALL come from the real text only

#### Scenario: Previously indexed sessions

- **WHEN** a session was indexed by a clio version without this rule
- **THEN** its stored text is unchanged until `clio index --full` re-ingests it; the
  README SHALL state this and that Claude Code's own transcript file is never edited
