## 1. Real-sample fixture

- [x] 1.1 Copy `gemini-real-sample-with-assistant.jsonl` (confirmed free of secrets) into
  `internal/ingest/testdata/gemini/tmp/gemini-sample-with-assistant/chats/
  session-2026-07-20T13-04-8bcf6c9f.jsonl`, byte-for-byte, as the ground-truth fixture
  driving this change.

## 2. Replay model — bare records via upsert-by-id

- [x] 2.1 Add an `idIndex map[string]int` (message id → position in the reconstructed list)
  alongside `reconstructed []geminiReplayedMsg` in `ParseFile` (`internal/ingest/gemini.go`).
- [x] 2.2 Replace the "any non-`$set` line is warn+skip+unparsed" branch: split into (a) a
  `$rewindTo` check (unchanged skip+count — still unobserved), and (b) a bare-record branch
  that decodes the line as a `geminiMessageEnvelope`, skip+counts a missing id (upsert has
  nothing to key on) or an envelope decode failure (defensive), and otherwise upserts by id
  — replace in place if the id is already in `idIndex`, else append and record its position
  (TDD).
- [x] 2.3 Rebuild `idIndex` wholesale (not merged) every time a `$set` overwrites
  `reconstructed`, so a bare record can never upsert into a position from a superseded
  `$set` generation (TDD).

## 3. Content decoding — array or bare string

- [x] 3.1 Add `geminiDecodeContent(raw json.RawMessage) []geminiContentBlock`: try the
  `content[]` array shape, then a bare JSON string (wrapped as a single block), else `nil`
  ("no extractable text") — replacing the duplicated array-only decode at both the `$set`
  element call site and the new bare-record call site (TDD).

## 4. Empty `messages` array — pin the choice

- [x] 4.1 No code change (falls out of the existing `json.Unmarshal` decode of `"[]"` into a
  slice); add `TestGeminiSetEmptyMessagesArrayClearsMessages` to pin the "empty array = full
  clear, not an abort" reading, including that a later bare-record upsert after the clear is
  a fresh append into a freshly-empty `idIndex`, and a doc comment in `ParseFile` recording
  the reasoning.

## 5. Tests, docs, ship-gate closure

- [x] 5.1 Invert `TestGeminiIngestBareAppendSkippedAndCounted` in place →
  `TestGeminiIngestBareAppendReplayedAndIndexed`: both the `$set`-carried message and the
  bare append are now indexed, in file order, `unparsed_lines` is 0.
- [x] 5.2 Add `TestGeminiBareRecordUpsertByIDReplacesAtOriginalPosition` (positional
  replace, not tail-move) and `TestGeminiBareRecordAppendMultipleDistinctIDsInOrder`
  (multi-append in file order).
- [x] 5.3 Add `TestGeminiMetadataOnlySetInterleavedWithBareAppendsDoesNotClear` (a
  metadata-only `$set` between bare-record upserts is a true no-op).
- [x] 5.4 Add two end-to-end tests against the real-sample fixture:
  `TestGeminiRealSampleWithAssistantMessageCountAndRoleOrder` (message count + role order)
  and `TestGeminiRealSampleWithAssistantEndToEndConversation` (real question + real reply
  both indexed with correct text, `<session_context>` wrapper neither indexed nor used as
  title).
- [x] 5.5 Update `gemini.go` doc comments (package-level `geminiSource` comment,
  `geminiMessage`/`geminiMessageEnvelope`, `errUnusableStateRecord`/`unusableStateCounter`,
  `ParseFile`'s replay-model comment) to describe the corrected model as confirmed, not
  provisional, and to stop describing bare records as unreplayed.
- [x] 5.6 Full regression: every pre-existing Gemini test stays green except the one
  inverted in 5.1; `go build/vet/test -race -count=1 ./...` green repo-wide; `gofmt -l .`
  clean; `openspec validate 2026-07-20-gemini-oplog-replay-from-real-sample --strict` green.
- [ ] 5.7 (Out of scope for this change, tracked for the future — see proposal.md
  "Non-goals") `thoughts[]`/`toolCalls[]` extraction into thinking/tool-use messages and
  activity targets, and `$rewindTo` replay, remain unbuilt. This change does not update
  `2026-07-17-add-gemini-cli-source`'s own task 6.1 checkbox or archive that change —
  left to the caller to reconcile across both changes.
