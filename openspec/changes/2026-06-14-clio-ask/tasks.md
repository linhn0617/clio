## 1. Retrieval core (`internal/ask`)

- [x] 1.1 Term extraction (TDD): strip bilingual stopwords / question words to
  content terms; fall back to all terms when the question is all stopwords.
- [x] 1.2 Any-term (OR) retrieval (TDD): add an any-term FTS match builder in
  `search` alongside the existing AND builder, and an `ask` retrieval that selects
  `m.seq` and reuses `commonFilters`; a message matching any single term is a
  candidate, ranked by relevance.
- [x] 1.3 Windowing (TDD): `sessions.GetWindow` returns ±N user/assistant turns
  around a hit seq (conversational turn space, not raw seq), excluding tool output
  by default.
- [x] 1.4 Assemble (TDD): group candidates by session, merge overlapping windows,
  re-rank sessions by aggregate score + recency, pack within a budget; produce
  `Answer{ Groups []EvidenceGroup{ Excerpts []Excerpt } }` with citations and
  hit marks.

## 2. Surface

- [x] 2.1 `clio ask` command (TDD): `--project` (default all), `--since`,
  `--limit`, `--window`, `--json`; readable grouped bundle; empty question →
  usage error; empty/missing index → clean empty answer; incremental catch-up like
  `search` (defers to a live MCP server).
- [x] 2.2 `ask` MCP tool (TDD): params question/project/since/limit; structured
  JSON bundle; read-only annotation; registered in the server.

## 3. Verify

- [x] 3.1 `go build/vet/test ./...` green (incl. `-race` + windows cross-build);
  `openspec validate --strict`.
- [ ] 3.2 Third-party (codex) review of the real implementation diff; fix
  findings; re-review to a clean gate.
