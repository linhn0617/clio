## MODIFIED Requirements

### Requirement: Parsers extract token usage per source
Ingest SHALL extract token usage during normal indexing for both sources, each producing a
deterministic aggregate from complete state: Claude Code runs a dedicated usage pass over the
session's **original source file** (extracting only outer uuid, `message.model`, and
`message.usage`; deduplicated by uuid with the later line winning; grouped by model) — the
filtered `messages` table MUST NOT be used as the usage source, because the parser excludes
clio-MCP tool-use events that still carry usage; Codex takes the latest cumulative
`total_token_usage` observed in the session's rollout file (one rollout file is one session —
no cross-file summing) and always writes the `(mixed)` model sentinel. The Claude usage scan SHALL run outside the write
transaction, SHALL cover exactly the committed complete-line watermark `[0, newOffset)` with
the same complete-line and line-cap semantics as text ingest, and the aggregate replacement
SHALL execute inside the commit transaction after message inserts and before commit — on the
main commit path only: the cross-source-conflict early-exit path SHALL be a usage no-op. A
usage scan that fails mid-pass SHALL be an atomic no-op: no replace, no delete, existing rows
retained. Codex `rate_limits` payloads SHALL be captured into quota
snapshots with the event timestamp as `observed_at` under the timestamp-guarded upsert.

#### Scenario: Claude duplicate uuids count once
- **WHEN** a Claude session file contains the same message uuid on multiple JSONL lines
- **THEN** the session's usage aggregate counts that uuid's usage exactly once, using the later
  line's values

#### Scenario: Claude usage on parser-excluded events still counted
- **WHEN** a Claude session contains an assistant event whose only content block is a clio MCP
  tool call (which produces zero message rows in the text index)
- **THEN** that event's `message.usage` is still included in the session's aggregate

#### Scenario: Claude incremental tail yields full-session totals
- **WHEN** a pre-existing indexed Claude session receives new messages via incremental ingest
- **THEN** the recomputed aggregate covers the entire source file, not only the new tail

#### Scenario: Codex cumulative counter not double-summed
- **WHEN** a Codex rollout contains successive `token_count` events with growing cumulative
  totals
- **THEN** the aggregate equals the latest cumulative value, not the sum of all events

#### Scenario: Codex tail without token events is a no-op
- **WHEN** an incremental read of a Codex rollout tail contains no `token_count` events
- **THEN** the session's existing usage row is left untouched

#### Scenario: Full rebuild without usage deletes stale row
- **WHEN** a full rebuild of a session file finds no usage events but a stale usage row exists
- **THEN** the stale row is deleted

#### Scenario: Cross-source conflict leaves usage untouched
- **WHEN** a file from a different source carrying usage is rejected as a session-UUID
  conflict (the early-exit commit path)
- **THEN** the existing session's `session_usage` rows are completely unchanged

#### Scenario: Uncommitted tail line not counted
- **WHEN** a Claude session file ends in an incomplete line beyond the committed watermark
- **THEN** the usage scan does not count it; it is picked up once text ingest commits it

#### Scenario: Failed scan is an atomic no-op flagged stale
- **WHEN** the usage pass encounters a hard mid-scan read error during a full rebuild
- **THEN** the session's previous usage rows are retained — neither partially replaced nor
  deleted — and the file's `usage_stale` flag is set until a successful rescan

#### Scenario: Oversized line skipped, scan continues
- **WHEN** a Claude session file contains a historical line exceeding the line cap
- **THEN** the usage scan skips it, counts it in `usage_skipped`, and later appends to the
  same file still update the usage aggregate

### Requirement: Usage diagnostics persist per file
`ingest_state` SHALL gain `usage_skipped` and `usage_unmapped` counters whose update semantics
follow the scope of the extraction pass: whole-file passes (Claude usage scan,
any full ingest) SHALL replace the counters with that pass's result; only tail-scoped
extraction (Codex incremental) accumulates. Surfaced by `clio doctor` alongside
`unparsed_lines`.

#### Scenario: Whole-file pass replaces counters
- **WHEN** a Claude file containing one malformed usage line receives three successive
  incremental appends (each triggering a whole-file usage scan)
- **THEN** `usage_skipped` remains 1, not 3

#### Scenario: Full re-ingest replaces counters
- **WHEN** a file with malformed usage events is fully re-ingested
- **THEN** its `usage_skipped` value reflects that run alone (replace, not accumulate)

### Requirement: Bounded storage growth and ingest cost
The usage tables SHALL add O(session-model pairs) rows only (bounded by a small constant
factor over sessions in practice). All gates below are measured under the
protocol in design.md (versioned fixture set, fresh DB, `wal_checkpoint(TRUNCATE)` before size
reads, main-DB size as page_count × page_size; two timing layers per design.md — end-to-end
CLI metrics via interleaved fresh-sandbox CLI invocations n≥10, component metrics via
order-counterbalanced Go benchmarks — comparing medians; absolute bounds are defined for the reference hardware class recorded in
perf/measurements.md and MUST be recalibrated proportionally to the measured baseline
in-process tail time on materially different hardware). On the reference fixture set:
post-checkpoint DB growth MUST be < 2%; full-index
wall-clock regression MUST be < 5%; on the 5,000-message long-session Claude reference fixture
the usage pass MUST add < 20 ms absolute to a single in-process tail ingest (the CLI-level
tail delta is recorded as informational — it proved an unstable metric, ~11-15 ms of real cost
plus machine noise over a denominator of mostly fixed command overhead), with write-lock hold
time increase < 10% one-sided measured in alternated pairs (the ~2× fixture
is an informational slope point, not a gate — the scan is O(file) per touch by design, and the
documented escalation path for longer sessions is a per-file usage cursor as its own change;
an in-process RELATIVE bound is unmeetable by construction over a ~3 ms baseline, see design); and the
doctor DB/source ratio MUST stay below the existing 3× warning threshold.

#### Scenario: Growth and throughput measured against numeric gates
- **WHEN** the reference fixture set is indexed with and without usage extraction under the
  measurement protocol
- **THEN** the recorded deltas are documented in the change dir and each is within its numeric
  gate above

#### Scenario: Long-session tail ingest stays within its gate
- **WHEN** a single incremental tail is ingested into the long-session Claude fixture with the
  usage pass enabled
- **THEN** the in-process absolute overhead is < 20 ms and write-lock hold time increase is
  < 10% (one-sided, alternated pairs), with the CLI-level delta and scanned bytes/rows recorded

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

#### Scenario: Stray opener before a complete element

- **WHEN** a message contains a lone `<private>` (no closer) and, later in the same
  message, a complete `<private>secret</private>`
- **THEN** the lone opener SHALL stay as literal text and the complete element SHALL
  still be removed, so `secret` is never stored

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

## REMOVED Requirements

### Requirement: Gemini CLI transcripts are discovered under the chats directory
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.

### Requirement: Gemini project path is resolved from projects.json
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.

### Requirement: Gemini session identity comes from the metadata line
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.

### Requirement: Gemini transcripts are reconstructed by replaying their $set op-log
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.

### Requirement: An unusable Gemini state record aborts the pass without losing prior state
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.

### Requirement: Gemini messages map to clio roles and strip harness context
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.

### Requirement: Nested Gemini transcripts are indexed as flat sessions
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.

### Requirement: Gemini appears across source surfaces via the registry
Reason: the Gemini CLI source adapter is removed (Gemini CLI's personal OAuth tier was discontinued upstream, so the adapter has no working target).

Migration: none for users; migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'` rows. A future Antigravity CLI adapter would be a new change.
