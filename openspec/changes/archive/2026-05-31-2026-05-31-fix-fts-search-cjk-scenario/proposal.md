## Why

`openspec/specs/fts-search/spec.md` states, under "Full-text search with trigram
tokenizer", that a 2-rune CJK query (`驗證`) matches "because the trigram tokenizer
matches sub-sequences". The code does not work that way: terms shorter than 3 runes
take the LIKE/substring fallback (`internal/search/query.go`,
`internal/search/search.go`), and the same spec's "Hybrid short/long term search"
requirement already says all-short queries fall back to a substring scan. The
trigram scenario is thus inaccurate and internally inconsistent. A codex review of
the README ranking/grep change surfaced it.

## What Changes

- **Modified** the `fts-search` capability: the "Full-text search with trigram
  tokenizer" requirement now scopes trigram matching to terms of 3+ runes, and the
  CJK example is split into a 3-rune trigram match and a 2-rune
  substring-fallback match — consistent with the implementation and with the
  existing "Hybrid short/long term search" requirement.
- Spec accuracy only. No code or behavior change.

## Capabilities

### Modified Capabilities

- `fts-search`: the trigram-tokenizer requirement distinguishes 3+ rune (trigram)
  queries from short (substring-fallback) queries, instead of claiming a 2-rune CJK
  query matches via trigram.
