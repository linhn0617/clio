## Why

`2026-07-17-add-gemini-cli-source` shipped the Gemini adapter with task 6.1 marked as a
**hard ship gate**: v1's replay model was built from a real transcript that had no
assistant turn, so it could only guess at unobserved shapes. It guessed that a bare
(non-`$set`) top-level message record is not part of the real op-log and warned, skipped,
and counted it as unparsed. Task 6.1 required re-confirming that guess against a real
multi-turn transcript before archiving the change or announcing Gemini as supported.

This change is that re-confirmation. A real gemini-cli transcript with a user question and
an assistant reply (`gemini-real-sample-with-assistant.jsonl`, captured 2026-07-20) shows
the guess was **wrong**: every message after the opening `<session_context>` `$set` in that
sample — the user's real question, both halves of the assistant's turn, and a tool
`functionResponse` fed back to the model — arrives as a bare top-level record, not inside a
`$set`. If bare records are truly skipped, essentially the entire conversation is invisible
to search. The real sample also revealed a second, unrelated wrong assumption: a bare
`"gemini"`-type record's `content` field is a bare JSON **string**, not the `content[]`
array of `{text}` blocks used by `$set`-embedded turns — the original baseline sample had
no assistant turn to reveal this either.

Ground truth (not provisional, not guessed): `gemini-real-sample-with-assistant.jsonl`, an
11-line real gemini-cli transcript. Lines 3, 5, 7, 8, 10 are bare records. Lines 5 and 7
share one id (`bb86a134…`) — the same assistant message arriving once with only `thoughts`,
then again with `toolCalls` added — proving a bare record is not a blind append but an
**upsert keyed by message id**.

## What Changes

- **Corrected** the Gemini op-log replay model: a bare (non-`$set`, non-`$rewindTo`)
  top-level message record is now replayed via **upsert-by-id** into the reconstructed
  message list — an id already present is replaced at its original position, an unseen id
  is appended — instead of being warned, skipped, and counted as unparsed. `$rewindTo`
  remains unobserved in any real transcript to date and is still skip+counted, not replayed.
- **Corrected** content decoding to accept the two real shapes confirmed by the sample: the
  `content[]` array of `{text}` blocks (unchanged, used by `$set`-embedded turns) and a bare
  JSON string (new, used by bare `"gemini"`-type records). Both feed the same downstream
  "no extractable text" handling per role; nothing else about role mapping, redaction,
  title derivation, or the abort/skip-and-count boundaries changed.
- **Pinned** a previously-undecided edge case in the replay model: a `$set` whose
  `"messages"` value is a present, non-null, but empty array (`[]`) is treated as a
  legitimate full clear (not an abort) — `$set` is inherently a full-replace operation, and
  `json.Unmarshal` never errors decoding `"[]"` into a slice, so no new code branch was
  needed, only a pinning test and a doc comment recording the choice.
- **Added** the real sample itself as a checked-in fixture
  (`testdata/gemini/tmp/gemini-sample-with-assistant/`), confirmed free of secrets, plus
  synthesized fixtures for upsert-by-id, multi-append, and metadata-only-`$set`
  interleaving.
- **Did not change**: discovery/ownership, project-path resolution, session identity,
  the unusable-`$set` abort contract, whole-file-replay/idempotency, nested-file flat
  indexing, or the registry wiring. `thoughts[]`/`toolCalls[]` extraction remains
  deliberately out of scope (still left, redacted, in `raw_json`) — only the
  reconstruction/replay model was re-confirmed against real bytes, not extraction.

## Capabilities

### Modified Capabilities

- `session-ingest`: corrects the `$set` op-log replay requirement's treatment of bare
  message records (skip+count → upsert-by-id replay) and its content-shape handling
  (array-only → array-or-string), and pins the empty-`messages`-array clear semantics.

## Non-goals

- `thoughts[]`/`toolCalls[]` extraction into thinking/tool-use messages and activity
  targets — their real shapes are now confirmed but extraction is a separate, larger piece
  of work deferred to a future change.
- `$rewindTo` replay — still unobserved in any real transcript to date.
- Subagent nesting + parent linking, agent-type presence — unrelated to this
  re-confirmation's scope (the real sample used here is a single main-session file, not
  nested).
- Re-running the spec-first adversarial review process from the original change — the user
  has directed direct implementation against the now-confirmed real schema.
