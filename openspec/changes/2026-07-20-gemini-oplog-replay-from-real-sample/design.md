# Design: correct the Gemini op-log replay model from a real assistant-bearing sample

This is the `2026-07-17-add-gemini-cli-source` change's task 6.1 ship gate: v1's replay
model was a documented guess (design.md §0 of that change, "Provisional... not in the live
sample") built before any real transcript contained an assistant turn or a bare record.
This change replaces the guess with the behavior a real transcript actually shows, and is
scoped narrowly to the replay/reconstruction model — extraction of `thoughts`/`toolCalls`
into separate messages remains out of scope (see proposal.md "Non-goals").

## 0. Ground truth (superseding v1's "Provisional" section)

`gemini-real-sample-with-assistant.jsonl`, an 11-line real gemini-cli transcript captured
2026-07-20, `internal/ingest/testdata/gemini/tmp/gemini-sample-with-assistant/chats/
session-2026-07-20T13-04-8bcf6c9f.jsonl`:

```
line 1  metadata (sessionId 8bcf6c9f…, kind: main)
line 2  $set  messages=[ {id d04923d3…, user, content: [{text: <session_context> wrapper}]} ]
line 3  bare  {id 3facfa74…, user, content: [{text: "Read hello.py and explain..."}]}
line 4  $set  {lastUpdated: ...}                          -- metadata-only, no "messages" key
line 5  bare  {id bb86a134…, gemini, content: "", thoughts: [...]}         -- content is a STRING
line 6  $set  {lastUpdated: ...}                          -- metadata-only
line 7  bare  {id bb86a134…, gemini, content: "", thoughts: [...], toolCalls: [...]}  -- SAME id as line 5
line 8  bare  {id 65935a51…, user, content: [{functionResponse: {...}}]}   -- no "text" field
line 9  $set  {lastUpdated: ...}                          -- metadata-only
line 10 bare  {id a3b4c61d…, gemini, content: "This script prints..."}     -- content is a STRING
line 11 $set  {lastUpdated: ...}                          -- metadata-only
```

Two things this sample proves, superseding v1's guesses:

1. **A bare top-level record IS part of the real op-log**, not an unobserved shape. 5 of the
   10 post-metadata lines in this sample are bare records — v1's "warn+skip+unparsed" for
   this shape would have discarded essentially the entire real conversation (only the
   `<session_context>`-wrapper `$set` and three metadata-only `$set`s would have survived).
2. **A bare record is not a blind append — it is an upsert keyed by message id.** Lines 5
   and 7 share the exact id `bb86a134-6675-4b69-a7f7-df498787e978`: the same assistant
   message, first written with only `thoughts`, then rewritten with `toolCalls` added. A
   naive "always append" model would have produced two separate assistant messages for one
   logical turn.

A third, unrelated fact this sample reveals: **a bare `"gemini"`-type record's `content`
field is a bare JSON string** (lines 5, 7, 10), not the `content[]` array of `{text}`
blocks used by every `$set`-embedded message observed so far (lines 2, 3, 8). The original
v1 baseline sample (`2026-07-17-add-gemini-cli-source`, the wrapper-only sample) had no
assistant turn at all, so it never exercised this shape.

`$rewindTo` remains unobserved in this sample too (and in the original baseline) — it stays
skip+counted, not replayed, per v1's original (still-correct-for-this-shape) treatment.

## 1. Replay model — upsert-by-id for bare records

The replay state is no longer just "the reconstructed messages array" — it is that array
PLUS an id→position index (`idIndex` in `internal/ingest/gemini.go`), so a bare record can
find and replace an existing entry in O(1):

```
state.messages = []                 // reconstructed conversation, in order
state.idIndex  = {}                 // message id -> position in state.messages

for each record after line 0, in file order:
  if record is {"$set": {messages: M, ...}}:
    state.messages = M              // full overwrite, last writer wins (unchanged from v1)
    state.idIndex  = rebuild from M // a $set is a full-state overwrite; a stale id from a
                                     // PRIOR generation must not be upsertable afterward
  elif record is {"$rewindTo": ...}:
    warn + skip + unparsed++        // unchanged from v1; still unobserved
  elif record has no id:
    warn + skip + unparsed++        // upsert-by-id has nothing to key on
  else:                             // bare record, has an id
    if record.id in state.idIndex:
      state.messages[state.idIndex[record.id]] = record   // upsert: replace AT ITS ORIGINAL
                                                            // POSITION, never moved to the tail
    else:
      state.idIndex[record.id] = len(state.messages)
      state.messages.append(record)                       // append: fresh id goes to the tail
```

Why replace-in-place and not "delete + re-append at the tail": lines 5/7 in the real sample
are the SAME logical turn arriving twice as it accumulates fields (`thoughts` first, then
`toolCalls` added) — re-appending would move that turn to wherever it happened to be
*last written*, corrupting conversation order relative to messages that arrived in
between (there is a `$set` metadata-only record, line 6, between the two writes — though in
THIS sample nothing else arrives between them; the general case, exercised by this change's
`TestGeminiBareRecordUpsertByIDReplacesAtOriginalPosition` fixture, has a distinct message
land between the two writes, and asserts it stays where it was first seen).

Why `idIndex` is rebuilt wholesale (not merged) on every `$set`, not just replaced when
`state.messages` is: a `$set` is documented (`2026-07-17` design.md §3, "last writer wins")
to be a **full-state overwrite** — any id that existed only in a prior generation and is
absent from the new array no longer represents part of the current conversation. If a later
bare record referenced that stale id, upserting into a position that no longer exists in
`state.messages` would be a bug (either a silent no-op into garbage or, worse, corrupting an
unrelated position after array reallocation). Rebuilding fresh from the $set's own array is
the only safe reading of "last writer wins" applied to the id index too.

A metadata-only `$set` (no `"messages"` key) remains a true no-op for both `state.messages`
AND `state.idIndex` — this is unchanged from v1 and is pinned by
`TestGeminiMetadataOnlySetInterleavedWithBareAppendsDoesNotClear`, extended from the
original `TestGeminiMetadataOnlySetIsNoOp` to interleave bare-record upserts across it.

## 2. Content decoding — array or string

`geminiDecodeContent(raw json.RawMessage) []geminiContentBlock` (`internal/ingest/
gemini.go`) tries, in order: (1) the `content[]` array-of-`{text}`-blocks shape (all
`$set`-embedded messages observed to date), (2) a bare JSON string (bare `"gemini"`-type
records observed in the real sample), (3) anything else — object, number, bool, or an
explicit empty string — returns `nil`, "no extractable text". This single helper replaced
duplicated array-only decode logic at both call sites (`$set` element decode and bare-record
decode), so both paths share one definition of "what counts as extractable content" instead
of drifting.

Every caller already treats a `nil`/empty result identically regardless of WHY it is empty
(absent field, explicit `""`, or unrecognized shape) — an assistant message with no
extractable text is warned/skipped/counted (spec: "An assistant message with an
unrecognized content shape is not indexed empty"); a user message with no extractable text
is simply not a turn. This existing v1 behavior needed no change, only a decode fix feeding
it the right blocks.

## 3. Empty `messages` array — pinned as a legitimate clear

Not observed in any real sample to date (both the original baseline and this change's
sample only ever set a present, non-empty array or omit the key). The task directive for
this change asked this to be resolved by reading the code, not guessed blind:
`json.Unmarshal` decoding a JSON `"[]"` into a Go slice never errors — it produces a
zero-length, non-nil slice — so `{"$set": {"messages": []}}` already fell through the
existing decode path as "zero elements to replay," i.e. a full clear, with **no code change
required**. This is the more defensible reading versus treating it as unusable/aborting:
`$set` is inherently a full-replace operation (design.md §3 of `2026-07-17`, "the last
`$set` wins"), so an explicitly empty array is just that same full-replace semantics
applied to "the empty conversation" — there is nothing structurally different about it from
any other `$set` with a shorter array than the previous one. `null` remains categorically
different and still aborts (existing finding-1 fix, unchanged): `null` cannot be
distinguished from a corrupted attempt to signal "no change," whereas `[]` is unambiguous
JSON. Pinned by `TestGeminiSetEmptyMessagesArrayClearsMessages`, which also exercises that a
later bare-record upsert after the clear is a fresh append (not a stale reference into the
cleared generation) — proving the empty-array $set correctly rebuilds `idIndex` to empty
too (§1).

## 4. What did NOT change

Discovery/ownership (`Owns`, `Roots`), `projects.json` inversion, `SessionIDFromPath`,
purge-via-DB-uuid-resolution, the unusable-`$set` abort contract (over-cap, unparsable,
present-but-null at either the `$set` or `messages` level, empty metadata `sessionId`), the
per-element/per-record vs per-record-structure abort boundary (finding 2), redact-once-per-
record sharing (finding 1 mechanism), whole-file-replay / idempotency, nested-file flat
indexing with no parent link, and the registry wiring are all unchanged and covered by
`2026-07-17`'s existing (still-green, unmodified in this change) test suite.

## 5. Testing strategy

TDD against the now-red `TestGeminiIngestBareAppendSkippedAndCounted` (inverted in place to
`TestGeminiIngestBareAppendReplayedAndIndexed`), plus new tests for: upsert-by-id
positional replacement (`TestGeminiBareRecordUpsertByIDReplacesAtOriginalPosition`),
multi-append in file order (`TestGeminiBareRecordAppendMultipleDistinctIDsInOrder`),
metadata-only-`$set` interleaving (`TestGeminiMetadataOnlySetInterleavedWithBareAppendsDoesNotClear`),
the empty-array pin (`TestGeminiSetEmptyMessagesArrayClearsMessages`), and the real sample
itself checked into `testdata/` with two end-to-end tests (message count/role order;
full-conversation reconstruction with title/content assertions). Every pre-existing Gemini
test (41 total before this change) was re-run and stays green except the one inverted test —
proof this change is additive to the abort/skip-and-count/redaction/idempotency contracts,
not a rewrite of them. `go test -race -count=1 ./...` is green repo-wide; `gofmt`/`go vet`
are clean.
