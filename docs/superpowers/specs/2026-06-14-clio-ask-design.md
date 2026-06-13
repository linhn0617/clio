# clio `ask` — Design (RAG-style retrieval over history)

- **Date:** 2026-06-14
- **Status:** Approved (brainstorming) — to be implemented via OpenSpec SDD + TDD, then codex review.
- **Origin:** roadmap feature #3, after #1 activity index (v0.4.0) and #2 ambient-recall hook (v0.5.0).

## Decisions (locked in brainstorming)

1. **Retrieval-only; the MCP client (Claude) synthesizes.** clio stays 100%
   local-first — no LLM, no network, no API key. `ask` does *smart retrieval* and
   returns a cited evidence bundle; Claude Code (already an LLM) writes the prose
   answer over MCP. *Rejected:* clio calling an external LLM (breaks the
   all-data-stays-local stance the README leads with); a hybrid pluggable backend
   (complexity without payoff for v1).
2. **Evidence unit = windowed conversational excerpts.** Each hit is returned with
   a window of surrounding user/assistant turns, grouped by session — not lone
   snippets — so the synthesizer has the actual Q&A context to ground on.
3. **Pipeline = a thin layer over existing search** (Approach 1). Reuse FTS5 +
   ranking; add OR-matching, grouping, windowing, budgeting. *Rejected:* a
   passage-level index (too heavy for v1); local embeddings (heavy deps, against
   clio's pure-Go / no-LLM identity) — revisit only if keyword recall proves weak.

## Surface

- **CLI:** `clio ask "<question>"` prints a readable evidence bundle, grouped by
  session (citation header + windowed excerpts, hit lines marked). Flags:
  `--project` (default: **all projects** — clio's cross-session/cross-project
  memory is the whole pitch), `--since`, `--limit` (max sessions), `--window N`
  (user/assistant turns each side of a hit; default 2), `--json`.
- **MCP:** new tool **`ask`** — params `question` (required), `project` / `since`
  / `limit` (optional) — returns the structured JSON bundle. The tool description
  instructs Claude to synthesize from the returned excerpts and cite session ids.
  Marked **read-only** (annotation, per v0.3.x tool-safety annotations).

## Retrieval pipeline (`internal/ask`)

`Ask(ctx, db, opt) (Answer, error)`, five steps:

1. **Term extraction.** Strip stopwords (bilingual: `how/did/the/...`,
   `的/了/嗎/怎麼/我們/那個/...`) to content terms. If the question is *all*
   stopwords, fall back to using every term (never retrieve on nothing).
2. **Candidate retrieval (OR).** Add an OR-mode match builder to `search`
   (today's `buildMatchQuery` ANDs terms — feeding a whole NL question would
   require one message to contain *every* content word and tank recall). A message
   matching *any* strong term is a candidate; BM25 floats those matching more /
   rarer terms. Reuse `commonFilters` (role / since / project / touched / tool /
   ran). The query also selects `m.seq`.
3. **Group + window.** Group candidates by `session_uuid`. For each hit, take a
   window of ±N turns **in user/assistant turn space** (not raw `seq` — otherwise a
   run of tool_use/tool_result events between a question and its answer blows the
   window). Merge overlapping windows within a session. New helper
   `sessions.GetWindow(...)`.
4. **Re-rank.** Order sessions by aggregate hit score + recency (reuse
   `adjustedScore` / `recencyBonus`).
5. **Budget + pack.** Defaults: up to ~6 sessions, a few windows each, per-excerpt
   content cap (~500–800 chars), total bundle cap that stays comfortably inside one
   tool result.

## Data shapes

```
Answer{ Question string; Groups []EvidenceGroup }
EvidenceGroup{ SessionUUID, Title, Project string; EndedAt int64; Score float64; Excerpts []Excerpt }
Excerpt{ Seq int; TS int64; Role, Text string; IsHit bool }
```

Citation = short session id + title + project + date, so the user can
`clio show <id>` to verify.

## Defaults

| Decision | Default |
|---|---|
| Scope | all projects; `--project` narrows |
| Window | ±2 user/assistant turns around each hit |
| Output | CLI readable text + `--json`; MCP returns JSON |
| Package boundary | new `internal/ask` over `search` primitives + new `sessions.GetWindow` |

## Error handling

- Empty question → CLI usage error / MCP error result.
- Missing DB / empty index / no matches → a clean empty answer ("no relevant
  history found"), not an error.
- Read-only connection (`db.OpenReadOnly`); `context` threaded throughout.

## Testing (TDD, red→green)

- Term extraction strips stopwords and keeps content terms; all-stopword question
  falls back to all terms.
- OR-match recall: a message containing only one of the terms is found (an AND
  match would miss it).
- Windowing: returns the surrounding user/assistant turns, merges overlapping
  windows, excludes tool output by default.
- Grouping / re-rank: sessions ordered by aggregate score; budget caps respected.
- Bundle assembly: citations present (id / title / date); hit lines marked; empty
  index → empty answer.
- MCP `ask`: returns the structured bundle and carries the read-only annotation.

## OpenSpec change

`2026-06-14-clio-ask`: **ADD** an `ask` capability (requirements + scenarios for
the retrieval bundle), **MODIFY** `cli-surface` (new `ask` command + flags),
**MODIFY** `mcp-server` (new `ask` tool, read-only annotation).
