## Context

A batch of isolated P3 fixes. Each is independent; grouped only to share one review/merge
cycle. (The `fallbackProjectPath` `_`-collision comment the review asked for already exists
in `internal/ingest/projectpath.go`, so it is not relisted.)

## Decision

### 1. Bounded large-file read (`internal/ingest/ingest.go`)

`readFrom` currently does `io.ReadAll(f)` from the offset — unbounded memory for a huge
file. Cap it; the incremental offset machinery already advances per pass, so a capped read
naturally chunks a large file across passes:

```go
const maxIngestReadBytes = 256 << 20 // 256 MiB per pass

func readFrom(f *os.File, offset, limit int64) ([]byte, error) {
    if _, err := f.Seek(offset, io.SeekStart); err != nil {
        return nil, err
    }
    return io.ReadAll(io.LimitReader(f, limit))
}
```

In `IngestFile`, when the unread remainder exceeds the cap, log it; and if the capped read
contains no complete newline (a single line larger than the cap), skip the file with a
warning rather than spin (offset can't advance):

```go
if size-startOffset > maxIngestReadBytes {
    ing.log.Warn("large source file; ingesting in chunks", "file", path, "remaining", size-startOffset)
}
buf, err := readFrom(f, startOffset, maxIngestReadBytes)
...
completeLen := lastCompleteNewline(buf)
if completeLen == 0 {
    if int64(len(buf)) >= maxIngestReadBytes {
        ing.log.Warn("source line exceeds max ingest read; skipping file", "file", path)
    }
    return 0, false, nil
}
```

Trade-off: a single line > 256 MiB is skipped (logged) instead of OOM-ing — acceptable;
such a line is pathological and previously risked crashing the process.

### 2. `show --json` alias + `--limit` (`internal/cli/show.go`)

Add a `--json` bool (forces `format = "json"`) and a `--limit` int (default
`defaultShowMessages = 100000`, replacing the literal). `--json` takes precedence over
`--format`. `GetMessages` already clamps `limit <= 0` to its own default.

### 3. Installer `.bak` cleanup on failure (`internal/claudeconfig/claudeconfig.go`)

The atomic rename keeps the original intact on failure, so the `.bak` is redundant on every
exit. Replace the success-only `os.Remove(backup)` with a `defer os.Remove(backup)` placed
right after the backup is created, so the chmod/rename/temp-write failure paths no longer
leak a `.bak`.

### 4. UTF-8 trim at the boundary (`internal/ingest/parser.go`)

`trimToValidUTF8` calls `utf8.ValidString(s)` (whole-string) in a loop. Decode only the
boundary rune:

```go
func trimToValidUTF8(s string) string {
    for len(s) > 0 {
        if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
            break // last rune is valid (size>1 keeps a real U+FFFD)
        }
        s = s[:len(s)-1]
    }
    return s
}
```

and symmetrically `trimLeadingToValidUTF8` with `utf8.DecodeRuneInString` trimming from the
front. Behavior identical; no longer revalidates the whole (up to 32 KiB) half on each step.

### 5. `excluded_tool_uses` per-Ingester cache + error logging (`internal/ingest/ingest.go`)

`loadExcludedToolUses` runs `SELECT … FROM excluded_tool_uses` for every file. Cache it on
the `Ingester` (loaded once; a `bool` distinguishes "loaded empty" from "unloaded"), and
after a successful commit append that file's new clio ids so later files see them:

```go
type Ingester struct {
    ...
    excluded       []string
    excludedLoaded bool
}
func (ing *Ingester) excludedToolUses() ([]string, error) {
    if ing.excludedLoaded {
        return ing.excluded, nil
    }
    ids, err := ing.queryExcludedToolUses() // the old body
    if err != nil {
        return nil, err
    }
    ing.excluded, ing.excludedLoaded = ids, true
    return ids, nil
}
```

In `IngestFile`, the load error is now logged (was silently dropped):

```go
if excluded, err := ing.excludedToolUses(); err != nil {
    ing.log.Warn("load excluded tool uses failed", "err", err)
} else {
    parser.Seed(excluded)
}
```

and after a successful commit (past the `errStaleSnapshot` check):
`if ing.excludedLoaded { ing.excluded = append(ing.excluded, parser.ClioToolUseIDs()...) }`.
The new ids are per-file-unique (each parser tracks only its own file's clio tool_use ids),
so no duplicates accumulate.

### 6. Walker error logging (`internal/ingest/walker.go`)

`WalkSessionFiles` swallows per-entry errors (`return nil`). Add a `log *slog.Logger`
parameter (nil → discard) and `log.Warn("skip unreadable entry", "path", path, "err", err)`.
Callers: `IngestAll` passes `ing.log`; `doctor.Run` passes nil (it has no logger).

### 7. `XDG_DATA_HOME` absolute-only (`internal/config/config.go`)

Per the XDG spec a relative `XDG_DATA_HOME` is ignored. Honor it only when absolute:

```go
if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" && filepath.IsAbs(xdg) {
    return filepath.Join(xdg, "clio"), nil
}
// else fall through to the platform default
```

### 8. `errors.Is(err, fs.ErrNotExist)` (`claudeconfig.go`, `db/db.go`)

Replace `os.IsNotExist(err)` with `errors.Is(err, fs.ErrNotExist)` at the two non-test
sites (`claudeconfig.load`, `db.Open` chmod). Add `io/fs` imports.

### 9. `format.go` → `display.go`; `titleFrom` uses `strings.Cut`

`git mv internal/cli/format.go internal/cli/display.go` (contents unchanged — they are CLI
display helpers). In `internal/ingest/parser.go`, `titleFrom` extracts the command name with
`strings.Cut` instead of `strings.Index`+slicing.

## Trade-offs / risks

- The read cap (256 MiB) chunks very large files across passes; a single line above it is
  skipped+logged (better than OOM).
- Adding a logger param to `WalkSessionFiles` touches its 2–3 callers; mechanical.
- The excluded cache lives for the `Ingester`'s lifetime (one per command; one for the
  watcher leader) and is kept consistent by appending committed ids — correct because the
  table only grows and ids are per-file-unique.
