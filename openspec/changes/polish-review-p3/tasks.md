## 1. show --json alias + --limit flag (TDD)

- [ ] 1.1 Failing test in `internal/cli/show_test.go`: `resolveShowFormat(format, jsonFlag)`
  returns `"json"` when `jsonFlag` is true (overriding `format`), else `format`.
- [ ] 1.2 Implement `resolveShowFormat`; add `--json` (bool) and `--limit` (int, default
  `defaultShowMessages = 100000`) to `newShowCmd`; use `resolveShowFormat` for the switch;
  convert `limit <= 0` to `defaultShowMessages` before `GetMessages` (so `--limit 0` does not
  fall into GetMessages' 50-row default); pass `limit` (replacing the literal `100000`).
  Green; default behavior unchanged when neither flag is set.

## 2. Installer .bak cleanup on failure (TDD via rename seam)

- [ ] 2.1 Add a package var seam `var renameFile = os.Rename` and route the rename through it.
- [ ] 2.2 Failing test in `internal/claudeconfig/claudeconfig_test.go`: set
  `renameFile` to a func returning an error (restore via `t.Cleanup`); `AddServer` on an
  existing config returns that error AND no `<path>.bak` remains AND the original file is
  unchanged.
- [ ] 2.3 Implement: replace the success-only `os.Remove(backup)` with `defer os.Remove(backup)`
  immediately after the backup is written. Green; existing `TestNoBackupLeftBehindOnSuccess`
  still passes.

## 3. UTF-8 trim at the boundary (TDD)

- [ ] 3.1 Failing test in `internal/ingest/parser_test.go`: `trimToValidUTF8` drops a trailing
  truncated multibyte sequence (`"ab"+string([]byte{0xE4,0xBD})` → `"ab"`), keeps a complete
  trailing rune (`"héllo"` unchanged) and a real U+FFFD (`"x"+string('�')` unchanged);
  `trimLeadingToValidUTF8` symmetric on a leading partial sequence; empty string → empty.
- [ ] 3.2 Implement both with `utf8.DecodeLastRuneInString` / `utf8.DecodeRuneInString`
  (keep a rune iff `r != utf8.RuneError || size > 1`). Green; existing `truncateForFTS` tests
  pass.

## 4. excluded_tool_uses load error logged

- [ ] 4.1 Implement: in `IngestFile`, log a `Warn` when `loadExcludedToolUses()` errors
  instead of silently skipping the seed. (No cache — that was dropped as racy/dup-prone.)
  Build + existing "exclude clio MCP traffic" test still green.

## 5. Walker error logging (all callers)

- [ ] 5.1 Implement: `WalkSessionFiles(projectsDir string, log *slog.Logger)` (nil →
  discard); `log.Warn("skip unreadable entry", "path", path, "err", err)` instead of silent
  `return nil`. Update every caller: `IngestAll` (`ing.log`), `doctor.Run` (`nil`),
  `watcher.handleEvent` (`w.log`), and any test caller (`nil`). `go build ./...` +
  `go test ./...` confirm no caller missed.

## 6. XDG_DATA_HOME absolute-only (TDD)

- [ ] 6.1 Failing test in `internal/config/config_test.go` (new): with
  `t.Setenv("XDG_DATA_HOME", "relative/dir")`, `DataDir()` does NOT return a path under
  `relative/dir` (falls back to the platform default); with an absolute value it returns
  `<abs>/clio`.
- [ ] 6.2 Implement: honor `XDG_DATA_HOME` only when `filepath.IsAbs`. Green.

## 7. errors.Is + rename + strings.Cut (refactors)

- [ ] 7.1 Replace `os.IsNotExist(err)` with `errors.Is(err, fs.ErrNotExist)` in
  `internal/claudeconfig/claudeconfig.go` (load) and `internal/db/db.go` (Open chmod); add
  `io/fs` imports. Behavior-preserving; existing tests cover.
- [ ] 7.2 `git mv internal/cli/format.go internal/cli/display.go` (contents unchanged).
- [ ] 7.3 `titleFrom` in `internal/ingest/parser.go` uses `strings.Cut`; add a `titleFrom`
  test (command-name case + plain-text case) in `internal/ingest/parser_test.go`.

## 8. Verify

- [ ] 8.1 `go test ./... -race -count=1` green.
- [ ] 8.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l .` empty.
- [ ] 8.3 Self-review, then codex re-review of the diff; address findings.
