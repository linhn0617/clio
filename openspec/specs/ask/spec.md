# ask Specification

## Purpose
TBD - created by archiving change 2026-06-14-clio-ask. Update Purpose after archive.
## Requirements
### Requirement: Retrieval-only evidence bundle for a question

`clio ask` SHALL return a retrieval-only *evidence bundle* answering a
natural-language question from indexed history, performing no text generation and
no network call. It SHALL extract content terms from the question, retrieve
messages matching any term, group them by session, attach to each hit a window of
surrounding user/assistant turns, rank sessions by relevance and recency, and pack
the result within a size budget. Each group SHALL carry a citation (session id,
title, project, date) and mark which excerpts matched.

#### Scenario: Question returns grounded, cited excerpts

- **WHEN** `clio ask "how did we fix the auth bug"` runs against indexed history
- **THEN** it SHALL return the most relevant sessions, each with windowed
  conversational excerpts and a citation, with matched excerpts marked

#### Scenario: Any-term retrieval keeps recall

- **WHEN** a question yields several content terms and no single message contains
  all of them
- **THEN** messages matching any term SHALL still be retrieved and ranked by
  relevance

#### Scenario: No relevant history is empty, not an error

- **WHEN** the index is empty or nothing matches the question
- **THEN** `clio ask` SHALL return an empty bundle and exit 0

#### Scenario: Generation is delegated to the client

- **WHEN** the bundle is produced
- **THEN** clio SHALL NOT synthesize prose or call any external service; the cited
  excerpts are returned for the caller to synthesize from

### Requirement: Evidence bundle is packed to a global token budget

The `ask` evidence bundle SHALL be packed to a total token budget over the excerpt text
it returns, in addition to the existing per-excerpt rune cap. Bundle size SHALL be
measured by a deterministic, dependency-free token estimator that counts CJK runes at
one token each and other runes at approximately four per token; the same estimator
SHALL be used wherever the budget is enforced or asserted. Packing SHALL add
whole ranked groups top-down and drop the lowest-ranked whole groups (LIKE-tier before
FTS-tier, then lowest aggregate score) once the budget is spent; a group SHALL never be
partially emitted, with one exception below. One invariant SHALL take precedence over
the budget: the top-ranked group's hit excerpts SHALL always be returned — when the
full top group does not fit, only its hit excerpts SHALL be emitted, each truncated to
fit but never below a minimum excerpt length. The bundle's estimated size SHALL
therefore not exceed the larger of the budget and the top group's minimum-length hit
excerpts. The budget SHALL default to a value suited to feeding an LLM's context (2000
tokens) and SHALL be configurable by the caller.

#### Scenario: A large history stays within the effective bound

- **WHEN** `clio ask` matches many long sessions and a small budget is set
- **THEN** the returned bundle's estimated token size SHALL NOT exceed the larger of
  that budget and the top group's minimum-length hit excerpts

#### Scenario: Whole lower-ranked groups are dropped first

- **WHEN** the budget is too small to hold every group
- **THEN** the lowest-ranked whole groups SHALL be dropped, in rank order from the
  bottom, and no dropped group SHALL be partially emitted

#### Scenario: A generous budget preserves the full bundle

- **WHEN** the budget is large enough for the whole assembled bundle
- **THEN** the bundle SHALL be identical to the pre-budget behavior (same groups and
  excerpts, each still capped at the per-excerpt rune limit)

#### Scenario: The keep-top-hits invariant outranks a tiny budget

- **WHEN** even the top-ranked group's hit excerpts at the minimum excerpt length
  exceed a very small budget
- **THEN** `ask` SHALL still return those hit excerpts (truncated to the minimum
  length), exceeding the budget rather than returning an empty bundle

