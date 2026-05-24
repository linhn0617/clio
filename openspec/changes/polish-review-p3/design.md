## Context

A batch of isolated P3 fixes, grouped only to share one review/merge cycle. Each is
independent and behavior-preserving except the two `show` flags and the installer backup
cleanup.

Dropped from the original list after codex plan-review:
- **Bounded large-file read** — capping `readFrom` and "chunking across passes" is incorrect
  with the current state machine: after a partial read `classifyChange` sees unchanged
  size/mtime and returns `changeSkip`, so the remainder never ingests; a single line over the
  cap blocks the file forever. A correct fix needs a pending-remainder state in the
  incremental machine (with truncation/fingerprint interactions) — a feature, deferred to its
  own change.
- **`excluded_tool_uses` per-Ingester cache** — the `*Ingester` is shared between `beforeRead`
  (`IngestAll`) and the leader loop's catch-up, so mutable cache fields would race; and
  `Parser.ClioToolUseIDs()` returns seeded ids too, so appending them per file grows the cache
  without bound. Only the error-logging part of that item is kept (below).
- The `fallbackProjectPath` `_`-collision comment already exists in `projectpath.go`.

## Decision

### 1. `show --json` alias + `--limit` (`internal/cli/show.go`)

Add `--json` (bool; forces JSON) and `--limit` (int; default `defaultShowMessages = 100000`,
replacing the literal). Extract the precedence into a tested helper:

```go
func resolveShowFormat(format string, jsonFlag bool) string {
    if jsonFlag {
        return "json"
    }
    return format
}
```

`--json` wins over `--format`. For `--limit`, a non-positive value falls back to the default
ceiling (the CLI converts it before calling `GetMessages`, so `--limit 0` does not hit
`GetMessages`' unrelated 50-row fallback):

```go
if limit <= 0 {
    limit = defaultShowMessages
}
msgs, _, err := sessions.GetMessages(database, sess.UUID, 0, limit, !noToolOutput)
```

### 2. Installer `.bak` cleanup on failure (`internal/claudeconfig/claudeconfig.go`)

The atomic rename keeps the original intact on failure, so the `.bak` is redundant on every
exit. Replace the success-only `os.Remove(backup)` with `defer os.Remove(backup)` right after
the backup is written (`os.Remove` of a missing file is harmless, so the no-backup paths are
fine). To make the post-backup failure path testable, route the rename through a package var
seam (mirroring the existing `preCommitHook` test pattern):

```go
var renameFile = os.Rename // overridable in tests
...
if err := renameFile(tmpName, configPath); err != nil {
    return err // defer removes the .bak
}
```

A test sets `renameFile` to return an error and asserts no `.bak` remains and the original is
intact.

### 3. UTF-8 trim at the boundary (`internal/ingest/parser.go`)

`trimToValidUTF8` calls `utf8.ValidString(s)` (whole-string) in a loop. Decode only the
boundary rune:

```go
func trimToValidUTF8(s string) string {
    for len(s) > 0 {
        if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
            break // last rune valid (size>1 keeps a real U+FFFD)
        }
        s = s[:len(s)-1]
    }
    return s
}
```

and symmetrically `trimLeadingToValidUTF8` with `utf8.DecodeRuneInString` from the front.
Behavior identical (empty string returns empty; a complete trailing rune or a real U+FFFD,
which decodes as `(RuneError, 3)`, is kept; only a truncated `(RuneError, 1)` byte is
trimmed).

### 4. `excluded_tool_uses` load error logged (`internal/ingest/ingest.go`)

Only the error-visibility part of the original item (no cache). In `IngestFile`, the swallowed
load error is logged:

```go
if excluded, err := ing.loadExcludedToolUses(); err != nil {
    ing.log.Warn("load excluded tool uses failed", "err", err)
} else {
    parser.Seed(excluded)
}
```

### 5. Walker error logging (`internal/ingest/walker.go`)

`WalkSessionFiles` swallows per-entry errors (`return nil`). Add a `log *slog.Logger`
parameter (nil → discard) and log skipped entries. **All three production callers** plus test
callers must pass the new argument:
- `internal/ingest/ingest.go` `IngestAll` → `ing.log`
- `internal/doctor/doctor.go` `Run` → `nil`
- `internal/watcher/watcher.go` `handleEvent` → `w.log`

```go
func WalkSessionFiles(projectsDir string, log *slog.Logger) ([]string, error) {
    if log == nil {
        log = slog.New(slog.NewTextHandler(io.Discard, nil))
    }
    ...
    if err != nil {
        log.Warn("skip unreadable entry", "path", path, "err", err)
        return nil
    }
    ...
}
```

### 6. `XDG_DATA_HOME` absolute-only (`internal/config/config.go`)

Per the XDG spec a relative `XDG_DATA_HOME` is ignored. Honor it only when absolute:

```go
if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" && filepath.IsAbs(xdg) {
    return filepath.Join(xdg, "clio"), nil
}
// else fall through to the platform default
```

### 7. `errors.Is(err, fs.ErrNotExist)` (`claudeconfig.go`, `db/db.go`)

Replace `os.IsNotExist(err)` with `errors.Is(err, fs.ErrNotExist)` at the two non-test sites
(`claudeconfig.load`, `db.Open` chmod). Add `io/fs` imports.

### 8. `format.go` → `display.go`; `titleFrom` uses `strings.Cut`

`git mv internal/cli/format.go internal/cli/display.go` (contents unchanged — CLI display
helpers). In `internal/ingest/parser.go`, `titleFrom` extracts the command name with
`strings.Cut` instead of `strings.Index`+slicing.

## Trade-offs / risks

- Adding a logger param to `WalkSessionFiles` touches its 3 production callers + test callers;
  mechanical, caught at compile time.
- The `renameFile` seam is a single package var defaulting to `os.Rename`, nil-free, only
  swapped in tests.
- All changes outside `show`/installer are behavior-preserving and covered by existing tests
  plus the few targeted unit tests below.
