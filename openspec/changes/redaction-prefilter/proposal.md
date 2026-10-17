## Why

Secret redaction is the dominant CPU cost of a full index. `internal/ingest/redact.go` runs 14
regular expressions over every message, title, tool summary and every string leaf of
`raw_json`; most of the rules have no literal prefix and several are case-insensitive, so Go's
regexp engine scans the whole text for each of them even though almost no message contains a
secret. A prototype of a cheap per-rule literal precheck cut a full index of the maintainer's
history from 40.5 s to 10.0 s with byte-identical output. The same string is also redacted up
to four times along the tool-call path (raw JSON walk, summary, activity target, message
content), which the precheck makes cheaper but does not remove.

## What Changes

- **session-ingest (B1, prefilter)**: each redaction rule carries a necessary literal feature
  (for example `AKIA`, `sk-`, `eyJ`, `://` plus `@`, or one of the secret-name keywords plus `:`
  or `=`); a rule whose feature is absent from the text is skipped. The output SHALL be
  byte-for-byte what running every rule would produce. Case-insensitive rules compare against a
  lower-cased copy; because Go's `(?i)` uses Unicode simple folding, the only non-ASCII runes
  that fold onto ASCII letters are U+212A KELVIN SIGN (`k`) and U+017F LONG S (`s`).
  `strings.ToLower` already maps U+212A to `k`, and the copy additionally maps U+017F to `s`,
  so non-ASCII text (for example Chinese) is filtered like any other. The rule table is
  validated at init (a `(?i)` rule must be marked and have lower-case needles; violations
  panic). A rule with no needles always runs.
- **session-ingest (B2, single pass)**: a tool call's summary and its activity target redact the
  same input string; they now share one `redactString` result (a one-entry memo). Output is
  unchanged. Other repeated passes are NOT removed: redaction is not idempotent in general (a
  JSON key redacted to a marker reads as a secret-named key on a second pass; a `KEY=value`
  capture can extend after an earlier match shrinks), so deleting them would change stored
  output. They are recorded as `debt:` in code.
- Tests: filtered vs unfiltered equivalence on every case in `redact_test.go` plus per-rule
  positives/negatives, Unicode folding and `\b` edges, a seeded random test and a Go fuzz target;
  a table test that keeps `foldCase` in sync with `(?i)`; a test pinning that only `k` and `s`
  fold onto non-ASCII runes. A one-off comparison over the maintainer's real index and source
  transcripts was run outside the repo.

## Impact

- No stored value changes. No schema, CLI or MCP change.
- Existing indexes need no re-index.
- Full-index CPU drops; numbers are in the change report, not promised here.
