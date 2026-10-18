## MODIFIED Requirements

### Requirement: Secret redaction during ingest

The system SHALL redact secret patterns before persisting content, covering both
free-text values (via shape patterns, including `Authorization: Basic`,
`Authorization: token` and `Cookie`/`Set-Cookie` headers) and structured JSON (via
key-name matching, including the `authorization` and `cookie` keys), and SHALL ensure
no secret reaches the session title. Each redaction rule MAY be gated by a cheap
necessary-condition check, but the stored output SHALL be exactly what running every
rule would produce.

The shape patterns SHALL include, besides the long-standing set (PEM private keys,
AWS/Google/GitHub/Slack/OpenAI-style/Stripe keys, JWTs, `Bearer` tokens, `KEY=value`
lines, credentialed connection strings): GitLab `glpat-` tokens, npm `npm_` tokens,
Hugging Face `hf_` tokens, Stripe webhook `whsec_` secrets, Slack incoming-webhook URLs,
PGP private key blocks, and `Authorization: token <x>` headers. A connection string
MAY have an empty user (`redis://:pass@host`). A `KEY=value` line SHALL be redacted
when the key ends in `_PASS` (for example `DB_PASS`) or is `MYSQL_PWD`, as it already is
for the `SECRET`/`PASSWORD`/`TOKEN`-family names; a bare `PWD` SHALL NOT be.

Four shapes are intentionally out of scope: a quoted password containing spaces (only its
first word is redacted), `curl -u user:pass`, `.netrc` `password` lines and Docker
`auths` blocks. They are recorded as a `debt:` in the rule table.

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

#### Scenario: Newly covered token and key shapes

- **WHEN** content contains `glpat-…` (20+ characters), `npm_` plus 36+ alphanumerics,
  `hf_` plus 34+ alphanumerics, `whsec_…`, a `https://hooks.slack.com/services/T…/B…/…`
  URL, a `-----BEGIN PGP PRIVATE KEY BLOCK-----` block, `Authorization: token <20+ chars>`,
  `redis://:pass@host`, `MYSQL_PWD=…` or `DB_PASS=…`
- **THEN** the system SHALL replace each with its `[REDACTED:<type>]` marker (the header
  name or key name kept where the rule keeps it)

#### Scenario: Ordinary text resembling the new shapes is untouched

- **WHEN** content contains prose such as `Authorization: token required for this endpoint`,
  identifiers such as `npm_config_registry` or `hf_hub_download`, a placeholder like
  `https://hooks.slack.com/services/TXXX/BXXX/xxxx`, a PGP public key block, `PWD=/home/u/p`,
  or `BYPASS_CACHE=enabled-for-all`
- **THEN** the system SHALL leave it unchanged

#### Scenario: Token used as a connection-string password

- **WHEN** content contains `a://u:p@sk-…` (or the same with a GitHub or AWS token)
- **THEN** the system SHALL produce exactly one `[REDACTED:connstring]` marker and no stray
  characters

#### Scenario: Key prefixed with an underscore

- **WHEN** content contains `x_sk-…` (an `sk-` key directly after an underscore)
- **THEN** the system SHALL redact the key and keep the `x_`; while `risk-assessment-…`,
  `disk-usage-…` and `sk-` after any letter or digit SHALL be left unchanged

#### Scenario: Secret key whose first letter is a non-ASCII fold of s

- **WHEN** content has `ſecret=xxxxxx` (U+017F LONG S) at the start of the text or after a
  space
- **THEN** the system SHALL redact the value as it does for `secret=`

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

#### Scenario: Embedded-JSON detection does time-bounded work

- **WHEN** a text field is a flood of brackets (for example 18 KB of `[`, or of `{"a":`)
- **THEN** the system SHALL finish in time linear in the text: it SHALL probe for embedded
  JSON only at a `{`/`[` that sits at the start of the text or right after whitespace, `:`,
  `,`, `=`, `(`, a quote (`"` or `'`), a backtick or a bracket, and SHALL stop probing after a
  fixed number of failed probes per text. Output for texts below those limits whose brackets
  all sit at such a boundary is identical to probing every bracket

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
to every source clio ingests (Claude Code, Codex), and SHALL never modify the
source JSONL. Tool inputs and tool results SHALL be walked the
same way when they are strings or string leaves. Removal SHALL run in time linear in
the length of the text, however many `<private>` openers it contains (thousands of
nested or unclosed openers on one line included), with exactly the same output as a
quadratic or cubic search would give.

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

#### Scenario: Stray opener before a complete element

- **WHEN** a message contains a lone `<private>` (no closer) and, later in the same
  message, a complete `<private>secret</private>`
- **THEN** the lone opener SHALL stay as literal text and the complete element SHALL
  still be removed, so `secret` is never stored

#### Scenario: Thousands of nested openers

- **WHEN** a single line holds 2000 `<private>` openers followed by one `</private>`
  (about 18 KB)
- **THEN** removal SHALL complete in well under a second (the earlier search took several
  seconds) and SHALL leave the same text as before: every opener but the last stays as
  literal text and the innermost complete element is removed

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
