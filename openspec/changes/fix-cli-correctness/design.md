## Context

Five independent, low-risk correctness fixes batched together (P2). Each touches one
function and is covered by a focused test. No shared state between them.

## Decision

### 1. `ResolvePrefix` exact-match-first (`internal/sessions/sessions.go`)

Split the single ambiguous query into two:

```go
const cols = `uuid, COALESCE(project_path,''), COALESCE(title,''), COALESCE(started_at,0), COALESCE(ended_at,0), turn_count`
// Exact match wins regardless of how many prefixes also match.
var s Session
err := database.QueryRow(`SELECT `+cols+` FROM sessions WHERE uuid = ?`, prefix).
    Scan(&s.UUID, &s.ProjectPath, &s.Title, &s.StartedAt, &s.EndedAt, &s.TurnCount)
switch {
case err == nil:
    return s, nil
case !errors.Is(err, sql.ErrNoRows):
    return Session{}, err
}
// No exact match: resolve by unique prefix (escaped, cap 2 to detect ambiguity).
rows, err := database.Query(`SELECT `+cols+` FROM sessions WHERE uuid LIKE ? ESCAPE '\' LIMIT 2`,
    db.EscapeLike(prefix)+"%")
// ... scan into matches ...
if err := rows.Err(); err != nil { return Session{}, err }
switch len(matches) { case 0: ErrNotFound; case 1: matches[0]; default: ErrAmbiguous }
```

Adds `database/sql` import (`errors`, `db` already imported). Exact match no longer
depends on `LIMIT` ordering; `_`/`%` in the prefix are escaped; `rows.Err()` is checked.

### 2. `show --format raw` adjacent dedup (`internal/cli/show.go`)

```go
case "raw":
    var prev string
    for i, m := range msgs {
        if i > 0 && m.RawJSON == prev {
            continue
        }
        fmt.Fprintln(os.Stdout, m.RawJSON)
        prev = m.RawJSON
    }
    return nil
```

`GetMessages` orders by `seq`, so rows expanded from one source line are adjacent and
collapse to a single printed line — the raw dump matches the source `.jsonl`.

### 3. `claudeconfig` reject non-object `mcpServers` (`internal/claudeconfig/claudeconfig.go`)

`serversMap` returns an error instead of silently discarding a non-object value:

```go
func serversMap(root map[string]any) (map[string]any, error) {
    v, ok := root["mcpServers"]
    if !ok || v == nil {
        return map[string]any{}, nil
    }
    m, ok := v.(map[string]any)
    if !ok {
        return nil, fmt.Errorf("mcpServers in config is not a JSON object (found %T); refusing to modify", v)
    }
    return m, nil
}
```

`AddServer`/`RemoveServer` call it inside the `mutate` closure (error returns before any
disk write, so the file is untouched); `HasServer` propagates the error.

### 4. `doctor` non-zero exit + no swallowed errors

`internal/cli/doctor.go`: extract a testable reporter that returns a sentinel error when
any check failed (root has `SilenceErrors`, so `main` prints `clio: …` to stderr and
exits 1):

```go
var errChecksFailed = errors.New("doctor: some checks reported warnings")

func reportDoctor(w io.Writer, results []doctor.Result) error {
    allOK := true
    for _, r := range results {
        mark := "ok  "
        if !r.OK { mark = "WARN"; allOK = false }
        fmt.Fprintf(w, "[%s] %-22s %s\n", mark, r.Name, r.Detail)
    }
    if !allOK {
        fmt.Fprintln(w, "\nSome checks reported warnings. Run `clio index --full` to rebuild if needed.")
        return errChecksFailed
    }
    return nil
}
```

`internal/doctor/doctor.go`: check the error on each `QueryRow(...).Scan(...)` and mark
the affected check failed rather than reporting a 0 count:
- FTS check: capture both the `messages` and `messages_fts` count errors.
- orphan sessions, ingest coverage (`tracked`), db size (`sourceBytes` → returns
  `(int64, error)`): on a scan error, add the check as failed with the error detail.
- `reconcile`: add a `rows.Err()` check after the row loop.

### 5. `activity_summary` local-day grouping (`internal/sessions/sessions.go`)

```go
case "day", "":
    keyExpr = "date(s.ended_at,'unixepoch','localtime')"
```

Verified: `modernc.org/sqlite`'s `localtime` modifier honors the process `TZ` and matches
Go's `time.Local`. Tests assert the bucket key equals `time.Unix(ts,0).Local().Format("2006-01-02")`
for timestamps that straddle a local-midnight boundary (deterministic in any timezone;
distinguishes from UTC grouping under a non-UTC `TZ`).

## Trade-offs / risks

- `ResolvePrefix` issues two queries instead of one on the prefix path; negligible and
  only when there is no exact match.
- `doctor` returning an error makes `main` print a terse `clio: …` line to stderr in
  addition to the stdout summary — intended, so scripts see a non-zero exit.
- `activity_summary` local-day depends on the server's timezone at query time; this is the
  desired behavior (the user's local day), and is documented in the scenario.
