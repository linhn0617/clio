## 1. Correct the spec

- [ ] 1.1 In the `fts-search` delta, restate "Full-text search with trigram tokenizer"
  so trigram matching is scoped to 3+ rune terms, with a 3-rune CJK trigram scenario
  and a 2-rune CJK substring-fallback scenario.

## 2. Verify

- [ ] 2.1 `openspec validate --strict` passes.
- [ ] 2.2 Scenarios match the code: 3+ rune terms drive the FTS/trigram index, all-short
  queries fall back to a substring scan (`internal/search/query.go`,
  `internal/search/search.go`).
