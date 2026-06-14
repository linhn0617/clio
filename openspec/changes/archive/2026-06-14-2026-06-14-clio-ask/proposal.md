## Why

clio can already *find* past conversations (`search`) and *read* one in full
(`read_session`), but answering a question from history is a manual loop: search,
open a session, scroll for the surrounding context, repeat across sessions.
Nothing assembles the relevant excerpts — with enough context to actually answer —
into one place. And the natural consumer, Claude Code, has to drive that loop
itself over multiple tool calls.

## What Changes

- **Added** `clio ask "<question>"`: a retrieval-only command (no generation) that
  returns a cited *evidence bundle* over indexed history — the conversation
  excerpts most relevant to the question, each with a window of surrounding
  user/assistant turns, grouped by session and ranked. clio performs no text
  generation and makes no network call: it extracts content terms from the
  question, retrieves messages matching any term (OR), windows and groups them,
  ranks by relevance + recency, and packs them within a budget. The CLI prints a
  readable bundle (or `--json`).
- **Added** an `ask` MCP tool returning the same bundle as structured JSON, so
  Claude Code synthesizes the answer from grounded excerpts and cites session ids.
  This keeps clio local-first (no LLM, no key, no cloud) while the client that is
  already an LLM does the writing. Marked read-only.

## Capabilities

### Added Capabilities

- `ask`: a retrieval-only, cited evidence bundle over history (windowed
  conversational excerpts, grouped by session, ranked, budgeted) for a
  natural-language question — consumed by the CLI and by an MCP tool for
  client-side synthesis.

### Modified Capabilities

- `cli-surface`: adds the `ask` command and its flags.
- `mcp-server`: adds the read-only `ask` tool.
