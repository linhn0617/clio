## Context

`internal/search/search.go`: `Search` dispatches to `ftsQuery` (raw query → `MATCH ?`)
or `likeQuery` based on `needsLikeFallback` (all-or-nothing on any <3-rune term).
`commonFilters` adds role/since/project filters; project uses `LIKE prefix+'%'`
unescaped. `internal/search/query.go` has `terms()` (quote-aware split) and
`needsLikeFallback`. Trigram FTS needs ≥3-char tokens.

## Decision

### 1. Literal-safe MATCH builder (`query.go`)
```go
func buildMatchQuery(terms []string) string {
    parts := make([]string, 0, len(terms))
    for _, t := range terms {
        parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
    }
    return strings.Join(parts, " ")
}
```
Each term becomes a quoted FTS5 phrase (AND semantics via space). Embedded `"` is
doubled. `c++`, `(`, `OR`, etc. are matched literally — no operator interpretation,
no syntax error. Used only for the ≥3-rune terms (trigram needs 3 chars).

### 2. Hybrid planner (`search.go`)
Partition `terms(opt.Query)` into `long` (≥3 runes) and `short` (<3 runes).
- **`len(long) > 0`** → one SQL query: `messages_fts MATCH <buildMatchQuery(long)>`
  plus, for each short term, `AND m.content LIKE ? ESCAPE '\'` (escaped). The FTS
  index narrows to few rows first; the LIKE on short terms filters only those rows
  (cheap, no full scan).
- **`len(long) == 0`** (all short, or empty) → pure `likeQuery` with every term as an
  escaped `LIKE` (existing path, now escaped).
Replaces `needsLikeFallback`.

### 3. LIKE escaping (`internal/db` + callers)
```go
// db.EscapeLike escapes %, _, and \ for use with LIKE ... ESCAPE '\'.
func EscapeLike(s string) string {
    r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
    return r.Replace(s)
}
```
Apply with `... LIKE ? ESCAPE '\'` and `EscapeLike(term)+"%"` etc. in:
- `search.go` content LIKE (short-term filters + pure-LIKE path)
- `search.go` `commonFilters` project_path LIKE
- `sessions.go` `ListSessions` project_path LIKE (same finding)

### 4. Graceful MATCH fallback (`search.go`)
The literal builder should prevent MATCH syntax errors. Defensively, if `ftsQuery`
returns an FTS error (`strings.Contains(err.Error(),"fts5")` / "syntax"), fall back to
`likeQuery` for the same options instead of surfacing the raw error.

### 5. Migration 0004 (`internal/db/migrations/0004_search_indexes.sql`)
```sql
CREATE INDEX IF NOT EXISTS idx_sessions_ended ON sessions(ended_at);
CREATE INDEX IF NOT EXISTS idx_messages_role_ts ON messages(role, ts);
```

## Trade-offs / risks
- Quoting every term as a phrase means multi-word phrase search still works (a quoted
  input `"foo bar"` is one term → one phrase). Bare `a b` becomes two AND phrases —
  same as today's term AND semantics.
- Short-term LIKE on FTS-narrowed rows is cheap because FTS runs first; verified by
  keeping the LIKE in the same WHERE as MATCH (SQLite evaluates MATCH via the index).
- Migration 0004 re-indexes on next open (one-time); additive, safe.
- `overscan`/post-rank behavior unchanged.
