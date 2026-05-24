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
    return writeRaw(os.Stdout, msgs)
...
func writeRaw(w io.Writer, msgs []sessions.Message) error {
    prev := false
    var last string
    for _, m := range msgs {
        if prev && m.RawJSON == last {
            continue
        }
        if _, err := fmt.Fprintln(w, m.RawJSON); err != nil {
            return err
        }
        last, prev = m.RawJSON, true
    }
    return nil
}
```

`ParseLine` computes `raw_json` once per source line and assigns it to every message
expanded from that line (text/thinking/tool_use/tool_result blocks), with consecutive
`seq`s; `GetMessages` orders by `seq`, so those rows are adjacent.

**Bounded contract (not "one line per source event").** The dedup rule is precisely
"collapse consecutive messages whose `raw_json` is identical into one printed line" — it
is intentionally not a claim to reconstruct distinct source events. Real Claude Code
events embed a unique `uuid`/`timestamp` in each line, so two *distinct* events never
share `raw_json`; and `raw_json` is explicitly display-grade (the session-ingest spec
allows normalized key order/whitespace). Adjacent-only (not global) dedup avoids
collapsing identical lines that are not contiguous. A guard test asserts that two adjacent
messages with *different* `raw_json` both print.

### 3. `claudeconfig` reject non-object `mcpServers` (`internal/claudeconfig/claudeconfig.go`)

`serversMap` returns an error instead of silently discarding a non-object value:

```go
func serversMap(root map[string]any) (map[string]any, error) {
    v, ok := root["mcpServers"]
    if !ok || v == nil { // absent or JSON null → safe to create a fresh map
        return map[string]any{}, nil
    }
    m, ok := v.(map[string]any)
    if !ok { // present, non-null, and not an object → meaningful data; refuse
        return nil, fmt.Errorf("mcpServers in config is not a JSON object (found %T); refusing to modify", v)
    }
    return m, nil
}
```

**`null` contract:** JSON `null` carries no server data, so it is treated as *absent* and
replaced with a fresh object (consistent with a missing key). A non-null, non-object value
(array, string, number, bool) is meaningful user data we must not clobber → error. The
spec scenario and tests pin both: `{"mcpServers":null}` is writable; `{"mcpServers":[]}`
and `{"mcpServers":"x"}` error and leave the file byte-for-byte unchanged.

`AddServer`/`RemoveServer` call it inside the `mutate` closure (the error returns before
any disk write, so neither the config nor a `.bak` is created); `HasServer` propagates the
error.

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
- `reconcile` → `reconcile(database) (missing, truncated, lag int, err error)`: return the
  error on `Query` failure, on a per-row `rows.Scan` failure, and from `rows.Err()`. The
  `source reconciliation` check is marked failed when `err != nil`. (Transient `os.Stat`
  errors that are not `IsNotExist` still `continue` — a filesystem hiccup on one source
  file should not fail the whole DB-health check; a missing file is already counted.)
  The existing `reconcile` tests update to the 4-value signature
  (`m, tr, _, _ := reconcile(d)`).

### 5. `activity_summary` local-day grouping (`internal/sessions/sessions.go`)

```go
case "day", "":
    keyExpr = "date(s.ended_at,'unixepoch','localtime')"
```

Verified by probe: `modernc.org/sqlite`'s `localtime` modifier honors the process `TZ`
(read at startup, like Go's `time.Local`).

**Deterministic, red-before-green test.** Because `time.Local` is fixed at process start, a
test cannot change it with `t.Setenv`, and under CI's usual `TZ=UTC` a local-vs-UTC test
cannot distinguish the fix (local == UTC). So the test **re-execs itself once in a child
process with `TZ=Asia/Taipei`** (UTC+8, no DST):

```go
func TestActivitySummaryLocalDay(t *testing.T) {
    if os.Getenv("CLIO_TZ_CHILD") == "" {
        cmd := exec.Command(os.Args[0], "-test.run", "^TestActivitySummaryLocalDay$", "-test.v")
        cmd.Env = append(os.Environ(), "TZ=Asia/Taipei", "CLIO_TZ_CHILD=1")
        if out, err := cmd.CombinedOutput(); err != nil {
            t.Fatalf("child failed: %v\n%s", err, out)
        }
        return
    }
    // Child (TZ=Asia/Taipei): ended_at = 2026-05-01 20:00:00 UTC = 2026-05-02 04:00 Taipei.
    // UTC day 2026-05-01, local day 2026-05-02 → the two implementations differ.
    ts := time.Date(2026, 5, 1, 20, 0, 0, 0, time.UTC).Unix()
    // ... seed one session with ended_at=ts + a message; ActivitySummary(d, ts-1, "day") ...
    want := time.Unix(ts, 0).Local().Format("2006-01-02") // "2026-05-02"
    // assert the single bucket key == want (UTC grouping would yield "2026-05-01" → red).
}
```

This is deterministic regardless of the parent's `TZ` and fails against the pre-fix UTC
query.

## Trade-offs / risks

- `ResolvePrefix` issues two queries instead of one on the prefix path; negligible and
  only when there is no exact match.
- `doctor` returning an error makes `main` print a terse `clio: …` line to stderr in
  addition to the stdout summary — intended, so scripts see a non-zero exit.
- `activity_summary` local-day depends on the server's timezone at query time; this is the
  desired behavior (the user's local day), and is documented in the scenario.
