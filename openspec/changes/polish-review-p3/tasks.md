## 1. Bounded large-file read (TDD)

- [ ] 1.1 Make the cap a package var for testability:
  `var maxIngestReadBytes int64 = 256 << 20`.
- [ ] 1.2 Failing test in `internal/ingest/ingest_test.go`: with `maxIngestReadBytes`
  temporarily lowered (save/restore via `t.Cleanup`), write a file of two newline-terminated
  lines whose first line's length sits below the cap and whose total exceeds it; the first
  `IngestFile` ingests line 1 and advances the offset, a second `IngestFile` ingests line 2
  (chunked across passes, no data loss). Also: a single line larger than the cap is skipped
  (`ingested == false`, 0 messages) and does not spin.
- [ ] 1.3 Implement `readFrom(f, offset, limit)` with `io.LimitReader`; in `IngestFile` warn
  when `size-startOffset > maxIngestReadBytes`, and when `completeLen == 0 &&
  len(buf) >= maxIngestReadBytes` warn "source line exceeds max ingest read; skipping file"
  and return skipped. Green.

## 2. show --json alias + --limit flag (TDD)

- [ ] 2.1 Failing test in `internal/cli/show_test.go`: `resolveShowFormat(format string,
  jsonFlag bool) string` returns `"json"` when `jsonFlag` is true (overriding `format`),
  else returns `format`.
- [ ] 2.2 Implement `resolveShowFormat`; add `--json` (bool) and `--limit` (int, default
  `defaultShowMessages = 100000`) flags to `newShowCmd`; use `resolveShowFormat` for the
  switch and pass `limit` to `GetMessages` (replacing the literal `100000`). Green; existing
  show behavior unchanged when neither flag is set.

## 3. Installer .bak cleanup on failure paths

- [ ] 3.1 Implement: in `mutate`, replace the success-only `os.Remove(backup)` with
  `defer os.Remove(backup)` right after the backup is written, so chmod/rename/temp-write
  failures no longer leak a `.bak`.
- [ ] 3.2 Regression: existing `TestNoBackupLeftBehindOnSuccess` still passes. (The
  post-backup failure paths — chmod/rename — cannot be induced deterministically in a unit
  test without a hook; the `defer` removes the backup on every path by construction.)

## 4. UTF-8 trim at the boundary (TDD)

- [ ] 4.1 Failing test in `internal/ingest/parser_test.go`: `trimToValidUTF8` drops a
  trailing truncated multibyte sequence (e.g. `"ab"+string([]byte{0xE4,0xBD})` → `"ab"`) but
  keeps a complete trailing rune and a real U+FFFD (`"x�"` unchanged);
  `trimLeadingToValidUTF8` symmetric on a leading partial sequence.
- [ ] 4.2 Implement both with `utf8.DecodeLastRuneInString` / `utf8.DecodeRuneInString`
  (keep a rune iff `r != utf8.RuneError || size > 1`). Green; existing `truncateForFTS`
  tests still pass.

## 5. excluded_tool_uses per-Ingester cache + error logging (TDD)

- [ ] 5.1 Failing test in `internal/ingest/ingest_test.go`: build an `Ingester`; call
  `excludedToolUses()`; insert a new row directly into `excluded_tool_uses`; a second
  `excludedToolUses()` returns the original cached set (proves it is cached, not re-queried).
- [ ] 5.2 Implement: add `excluded []string` + `excludedLoaded bool` to `Ingester`;
  `excludedToolUses()` lazy-loads once (keep the existing query body as
  `queryExcludedToolUses`); in `IngestFile` log a `Warn` on load error instead of swallowing,
  and after a successful commit append `parser.ClioToolUseIDs()` to the cache. Green; the
  existing "exclude clio MCP traffic" end-to-end test still passes.

## 6. Walker error logging

- [ ] 6.1 Implement: `WalkSessionFiles(projectsDir string, log *slog.Logger)` (nil →
  discard); `log.Warn("skip unreadable entry", "path", path, "err", err)` in the callback
  instead of `return nil` silently. Update callers: `IngestAll` passes `ing.log`,
  `doctor.Run` passes `nil`. (Triggering a walk error needs an unreadable dir, which is
  uid-dependent; covered by build + existing walk tests.)

## 7. XDG_DATA_HOME absolute-only (TDD)

- [ ] 7.1 Failing test in `internal/config/config_test.go` (new if absent): with
  `t.Setenv("XDG_DATA_HOME", "relative/dir")`, `DataDir()` does NOT return a path under
  `relative/dir` (falls back to the platform default); with an absolute value it returns
  `<abs>/clio`.
- [ ] 7.2 Implement: honor `XDG_DATA_HOME` only when `filepath.IsAbs`. Green.

## 8. errors.Is(fs.ErrNotExist) + 9. rename + strings.Cut (refactors)

- [ ] 8.1 Replace `os.IsNotExist(err)` with `errors.Is(err, fs.ErrNotExist)` in
  `internal/claudeconfig/claudeconfig.go` (load) and `internal/db/db.go` (Open chmod); add
  `io/fs` imports. Behavior-preserving; existing tests cover.
- [ ] 8.2 `git mv internal/cli/format.go internal/cli/display.go` (contents unchanged).
- [ ] 8.3 `titleFrom` in `internal/ingest/parser.go` uses `strings.Cut` for the
  `<command-name>…</command-name>` extraction; add/confirm a `titleFrom` test for the
  command-name case and a plain-text case.

## 10. Verify

- [ ] 10.1 `go test ./... -race -count=1` green.
- [ ] 10.2 `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
  `GOOS=windows GOARCH=amd64 go build ./...` clean; `gofmt -l .` empty.
- [ ] 10.3 Self-review, then codex re-review of the diff; address findings.
