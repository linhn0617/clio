## 1. Response budget

- [x] 1.1 Tests first: truncation at row boundary with `truncated`/`next` per tool, `max_chars`
      clamp, lone oversized row, default below 50k, subagent-message last resort
- [x] 1.2 `fitBudget` (serialized-size binary search over row count) and `max_chars` parameter on
      all five tools

## 2. read_session modes

- [x] 2.1 Tests first: `last_n`, `around_sequence` ranges and edges, mutual exclusion error,
      `offset: 0` not a mode, `total`/`offset` reporting
- [x] 2.2 `sessions.CountMessages`, mode selection in `handleReadSession`, tool schema

## 3. Docs and specs

- [x] 3.1 README.md / README_zh-TW.md
- [x] 3.2 Spec delta for mcp-server

## 4. Verification

- [x] 4.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `openspec validate --all --strict`

## 5. Round three (review fixes)

- [x] 5.1 `ask` hit-first test whose data an in-order cut cannot pass (negative control: in-order
      ranking turns it red)
- [x] 5.2 A row whose fixed fields alone exceed the budget falls back to zero rows with a
      fixed-fields hint measured on the final text; realistic-size data in the every-tool test
      without the over-budget relaxation; the tools that may take this path are listed
      (search, list_sessions, activity_summary project/usage, ask)
- [x] 5.3 Subagent list cap measured on its entries without inlined messages, and applied only
      when the whole response does not fit
- [x] 5.4 Parent page shrinks before inlined subagent messages are dropped; they go only when one
      parent message does not fit beside them
- [x] 5.5 `last_n` / `around_sequence` accept numeric strings via the same coercion as `offset`,
      `limit` and `max_chars`
- [x] 5.6 `next` joins every reason (dropped rows, cut text, subagent notes); read_session names
      the requested page size
- [x] 5.7 Budget counted in UTF-16 code units everywhere (measure, binary searches, `capText`,
      hints, tool description), 48,000 cap kept; emoji test
- [x] 5.8 read_session count and page fetch in one read-only transaction; plain offset mode
      derives `total` from a page that reaches the end instead of counting
- [x] 5.9 Reuse serialized results; skip the text-cut search for rows with no cuttable text;
      README notes a single row's text may be truncated; `note` for a hidden `around_sequence`
      target
- [x] 5.10 Verifier follow-up: the subagent-message shed decision measures one parent message as
      sent (truncated/next hints included), with a regression sweep over the subagent message
      size; `around_sequence` on an empty session says the session has no messages; the hidden
      target note is accurate when no dialogue message follows or none exists
