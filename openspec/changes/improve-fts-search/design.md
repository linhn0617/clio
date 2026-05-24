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
Empty queries are still rejected up front (unchanged `Search` guard) — the planner
only runs on a non-empty query, so `likeQuery` always has ≥1 term.
Partition `terms(opt.Query)` into `long` (≥3 runes) and `short` (<3 runes).
- **`len(long) > 0`** → one SQL query: `messages_fts MATCH <buildMatchQuery(long)>`
  plus, for each short term, `AND m.content LIKE ? ESCAPE '\'` (escaped). The FTS
  index narrows to few rows first; the LIKE on short terms filters only those rows
  (cheap, no full scan).
- **`len(long) == 0`** (all short, or empty) → pure `likeQuery` with every term as an
  escaped `LIKE` (existing path, now escaped).
Replaces `needsLikeFallback`.

Single-query shape (not a pre-LIMITed FTS subquery): `WHERE messages_fts MATCH ?
AND m.content LIKE ? ESCAPE '\' ... ORDER BY bm25 LIMIT ?`. The `LIMIT` applies
after the short-term `LIKE`, so there is no early-LIMIT-before-filter problem. The
FTS-first plan is verified with `EXPLAIN QUERY PLAN` (a test asserts the plan uses
the FTS virtual table), not merely assumed.

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
(`sessions.go` `ResolvePrefix` also has a user `LIKE`, but that is fixed in B3
`fix-cli-correctness`, not here.)

### 4. No silent FTS fallback
The literal builder makes MATCH syntax errors impossible, so there is NO
broad error→LIKE fallback. Any remaining FTS error (e.g. a missing/corrupt FTS
table) is an operational fault and SHALL surface as an error rather than being
masked by an expensive full scan.

### 5. Migration 0004 (`internal/db/migrations/0004_search_indexes.sql`)
```sql
CREATE INDEX IF NOT EXISTS idx_sessions_ended ON sessions(ended_at);
```
(`idx_messages_role_ts` dropped: the pure-LIKE path full-scans `content` regardless,
so a `(role, ts)` index would not be used. Add it later only if `EXPLAIN QUERY PLAN`
shows a need.)

## Trade-offs / risks
- Quoting every term as a phrase means multi-word phrase search still works (a quoted
  input `"foo bar"` is one term → one phrase). Bare `a b` becomes two AND phrases —
  same as today's term AND semantics.
- Short-term LIKE on FTS-narrowed rows is cheap because FTS runs first; verified by
  keeping the LIKE in the same WHERE as MATCH (SQLite evaluates MATCH via the index).
- Migration 0004 re-indexes on next open (one-time); additive, safe.
- `overscan`/post-rank behavior unchanged.
