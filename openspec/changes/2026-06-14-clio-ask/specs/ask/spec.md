## ADDED Requirements

### Requirement: Retrieval-only evidence bundle for a question

`clio ask` SHALL return a read-only, retrieval-only *evidence bundle* answering a
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
