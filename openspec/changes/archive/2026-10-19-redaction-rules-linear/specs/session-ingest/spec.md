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
MAY have an empty user (`redis://:pass@host`) and passwords containing `/`, `:` or
`@`, ending at the last `@` before the host. Connection strings SHALL run after
existing token/header/key rules and consume any prior redaction markers whole,
so query tails and adjacent Authorization headers cannot expose secrets.
A `KEY=value` line SHALL redact PASSPHRASE, `_PWD`, and `_PASS` with a
credential-context prefix: DB, MYSQL, SMTP, REDIS, ADMIN, ROOT, USER, MAIL, FTP,
SSH, LDAP, PG or POSTGRES, optionally preceded by another underscore-separated
prefix (MY_DB_PASS, APP_ROOT_PWD). The same restrictions SHALL apply to structured
keys. OLD_PWD and PROJECT_PWD SHALL remain unchanged. Bare PWD and computation-stage names such as first_pass,
FIRST_PASS, RENDER_PASS, SECOND_PASS, TEST_PASS, NUM_PASS and PASS_THROUGH SHALL
remain unchanged. BYPASS, COMPASS and ENCOMPASS SHALL remain unchanged.

Four shapes are intentionally out of scope: a quoted password containing spaces (only its
first word is redacted), `curl -u user:pass`, `.netrc` `password` lines and Docker
`auths` blocks. They are recorded as a `debt:` in the rule table. Truncated PEM/PGP blocks without
an END fence remain debt, to address if a truncated private key is ever found in
the index.

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
- **THEN** the system SHALL use a linear string/escape-aware pass to record balanced
  bracket spans; mismatched closers terminate candidates. Balanced syntax SHALL
  be checked once bottom-up, without repeated decoding of invalid nested spans.
  Decoder probes SHALL be restricted to balanced, syntactically valid candidates,
  with outermost candidates first and successfully decoded spans skipped.
  There SHALL be no prefix-boundary restriction or failed-probe cap. Span storage SHALL be sparse
  in opener offsets; non-bracket text SHALL NOT require a per-byte span table. Redacted
  positions SHALL cover main commit 165f184, verified by an independent test-only
  oracle over at least 10,000 generated inputs, including every ASCII punctuation
  prefix, random bracket noise, nested secrets, rule-order regressions and every
  token rule crossed with key/header rules, glued and separated.

#### Scenario: Embedded JSON at arbitrary positions

- **WHEN** JSON follows `->`, `<pre>`, `|`, `;`, `**`, `-d`, a word, CJK, accented
  text, 128 stray openers or 130 bracketed log lines
- **THEN** its secret-keyed values SHALL be redacted whole as on main, including
  a token value containing a space and a nested `secret=…` assignment

#### Scenario: Token alphabets and documentation guards

- **WHEN** Stripe webhook secrets contain base64 `+`, `/`, `=`, GitLab tokens end
  in `-`, or Slack webhook hosts have uppercase letters
- **THEN** the complete credential SHALL be redacted up to the token alphabet's
  terminator, with prefilters remaining necessary conditions
- **AND** obvious GitLab placeholders, Slack's official all-X webhook example,
  `Authorization: token YOUR_PERSONAL_ACCESS_TOKEN_HERE`, dotted Hugging Face
  identifiers and ordinary `task_sk-learn-…` words SHALL remain intact, provided
  a guarded word does not contain a later standalone credential that main removed

#### Scenario: AWS key adjacent to a key name

- **WHEN** an AWS access key id immediately precedes `secret=abcdefgh`
- **THEN** both the id and the assigned value SHALL be redacted

#### Scenario: Glued token and following credential

- **WHEN** a token or widened KEY=value match absorbs a following credential key
  or Authorization, Proxy-Authorization, Cookie or Set-Cookie header name
- **THEN** the original key/header removal SHALL still be evaluated and its value
  SHALL remain removed, including when the overlapping removals are coalesced
- **AND** original connection spans SHALL participate in that coverage merge so
  a consumed scheme cannot expose credentials; preserved key-name captures SHALL
  be suppressed where they overlap a connection removal

#### Scenario: Original match context and whole placeholders

- **WHEN** a guarded match depends on a Unicode-adjacent word boundary
- **THEN** the guard and replacement SHALL use original-string submatch indices
  without re-matching a slice
- **AND** a documentation placeholder SHALL be recognized only when its whole
  body after the fixed prefix consists of x/X, underscores and dashes, or an
  uppercase YOUR_..._HERE word sequence; a real body ending in -x SHALL be redacted

#### Scenario: AWS termination and connection port paths

- **WHEN** an AWS-shaped string has more than sixteen body characters without a
  word boundary or a following lowercase key start
- **THEN** it SHALL remain unchanged, including extended all-X and EXAMPLE values
- **AND** connection URLs with numeric ports and paths containing at-signs SHALL
  remain unchanged, including single-label hosts and IPv6; empty-user passwords
  and slash/colon passwords SHALL still be redacted when followed by a host
- **AND** localhost:3000 followed immediately by an at-sign and host SHALL retain
  main's redaction coverage

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

#### Scenario: Case folding keeps case-insensitive rules exact

- **WHEN** text spells a secret keyword with a rune Go's `(?i)` folds onto an ASCII letter
  (U+212A for `k`, U+017F for `s`, e.g. `API_\u212aEY=supersecretvalue123`), or contains other
  non-ASCII text such as Chinese
- **THEN** the secret SHALL be redacted exactly as without a prefilter, and text with no secret
  feature SHALL still skip the rules that cannot match

#### Scenario: A tool call's input is redacted once for its summary and target

- **WHEN** one tool call's input string feeds both its parameter summary and its activity target
- **THEN** the system SHALL redact that string once and use the same result for both, with
  output identical to redacting twice; passes that would otherwise change output (redaction is
  not idempotent on every input) SHALL NOT be dropped

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
