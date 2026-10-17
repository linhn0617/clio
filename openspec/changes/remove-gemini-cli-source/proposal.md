## Why

Gemini CLI's personal OAuth tier has been discontinued upstream (a live request now
returns `This client is no longer supported for Gemini Code Assist for individuals ...
migrate to the Antigravity suite`). The `gemini` source adapter (~850 lines plus ~2,160
lines of tests, six recorded debts) therefore has no working target, and the maintainer's
real index holds only three Gemini sessions. Keeping it costs maintenance with no users.

## What Changes

- **session-ingest**: remove the Gemini source adapter and its eight Gemini requirements;
  drop `gemini` from the source registry seed (so `--source`, the MCP `source` enums, the
  TUI label and `doctor` root report no longer list it), from the config helper
  (`GeminiTmpDir`) and from the model constants. Token-usage, diagnostics, storage-gate and
  private-tag requirements stop naming Gemini.
- **usage-summary / raw-json-retention**: drop the Gemini clauses and scenarios.
- **DB**: migration `0014_drop_gemini.sql` deletes previously indexed `source = 'gemini'`
  sessions, their messages (FTS follows via the `messages_ad` trigger), tool calls/targets,
  usage rows, `ingest_state` watermarks (by session `source_file` or `.gemini/tmp/` path) and
  `source_conflicts` entries.
- Kept deliberately: the generic source SPI (`WholeFileReplay()` and its `IngestFile`
  branch, the `errUnusableStateRecord` abort path). No built-in source uses them today; they
  stay as SPI surface with a fake-source test (`wholefilereplay_test.go`). Recorded as debt.

A future Antigravity CLI adapter would be a separate change.

## Impact

- Users with a `~/.gemini` directory: nothing is read from it any more; existing indexed
  Gemini rows are deleted on the next run that opens the database.
- `clio --source gemini` is rejected as an invalid source.
