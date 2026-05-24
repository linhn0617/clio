## Why

The code review (internal + codex) found three search problems:

1. **FTS queries error on common input.** `ftsQuery` passes the raw user query straight
   into `messages_fts MATCH ?` (`search.go:93`). Inputs like `c++`, `"unclosed`,
   `foo OR`, or `(test` are interpreted as FTS5 operators or are syntax errors, which
   surface a raw SQLite error to the user / MCP caller.
2. **Mixed-length queries degrade to a full table scan.** `needsLikeFallback`
   (`query.go:57`) routes the WHOLE query to `LIKE` if ANY term is < 3 runes, so
   `auth ui` or `go 繞過` scan all of `messages.content` even though the long terms are
   FTS-indexable.
3. **LIKE wildcards are not escaped.** Query terms and the `--project` prefix go into
   `LIKE` patterns unescaped (`search.go:106,81`, `sessions.go:52`), so a `%` or `_`
   in the input silently over-matches.

There is also no index on `sessions.ended_at`, which `list_sessions` /
`activity_summary` order and filter by.

## What Changes

- **Added** a literal-safe FTS MATCH builder: tokenize, escape `"`, wrap each term as a
  quoted phrase. User text is matched literally; no FTS5 operator surprises or syntax errors.
- **Modified** the planner to a hybrid: terms ≥ 3 runes drive the FTS index; terms
  < 3 runes are applied as `LIKE` filters on the FTS-narrowed rows (not a full scan).
  Only an all-short query falls back to a pure `LIKE` scan.
- **Added** `db.EscapeLike` and `ESCAPE '\'` to all `LIKE` clauses (content + project
  prefix in search and `list_sessions`).
- **Added** migration `0004` with `idx_sessions_ended` (and `idx_messages_role_ts`).
- **Modified** FTS execution to fall back gracefully if a MATCH ever still errors.

## Capabilities

### Modified Capabilities

- `fts-search`: literal-safe MATCH, hybrid FTS/LIKE planning, wildcard escaping, and a
  supporting index.
