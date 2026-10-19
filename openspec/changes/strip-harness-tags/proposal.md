## Why

Claude Code writes harness text into user-role transcript messages, and clio indexes
it as if the user typed it, so `search`, `ask` and `recall` match harness text as "the
user said". On the maintainer's index, of 3,839 user-role messages, 468 contain
`<task-notification>`, 299 `<system-reminder>` and 187 `<agent-message>` (the
categories overlap). Session titles are not polluted by a leading tag, but a short
prompt with an appended reminder put the reminder into the 100-rune title.

## What Changes

- **session-ingest**: user-role text has every complete `<system-reminder>`,
  `<task-notification>` and `<agent-message …>` element removed, content included,
  after redaction and before the emptiness check. A relayed agent message also loses
  the fixed `Another Claude session sent a message:` line before it and the fixed
  `That "other Claude session" is an agent …` paragraph after it, on an exact match only.
- The match is narrow so prose survives: the opener starts a line, the closer ends a
  line, the tag name is fixed and lower-case, and elements nest strictly (a closer only
  closes the innermost open element). Unclosed, mismatched or inline tags stay as
  literal text and never swallow what follows them. Linear time.
- A user message, or a user text block, that is nothing but harness elements is not
  stored, as a `<private>`-only message already is. The session title skips it too and
  is taken from the prompt alone.
- `raw_json` is untouched (it is still the redacted event). Assistant text, tool
  results and Codex transcripts are unchanged.

Shapes were taken from a real export of the maintainer's transcripts. Left alone, with
reasons:

- `[SYSTEM NOTIFICATION…]` banners and `recommended_plugins`: no instance in the export,
  so their exact shape and extent are unknown; they are removed only when they sit
  inside one of the three elements.
- A `[Subagent hand-back]` frame: only seen as the first line inside an
  `<agent-message>`, so it goes with that element.
- `<local-command-stdout>`, `<bash-input>`, `<bash-stdout>`, `<command-name>` and
  `<local-command-caveat>`: they record commands the user ran, and the existing
  handling (title only) is kept.

## Impact

- Search, ask and recall stop returning harness text as user text; turn counts drop by
  the harness-only turns.
- Existing indexes keep the old text until `clio index --full`.
- No schema, CLI or MCP change.
