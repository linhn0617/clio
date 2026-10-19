## 1. Read-only helpers

- [x] 1.1 Tests first, then `lock.Inspect` (absent / malformed / fresh / stale, never writes)
- [x] 1.2 Tests first, then `claudeconfig.ClioHookCommands` and `LookupServer`

## 2. Doctor

- [x] 2.1 Tests first: `--quick` skips the three heavy checks, runs no `quick_check`, needs no writable connection
- [x] 2.2 Tests first: hook binary / mcp registration / mcp lease / index freshness against temp files
- [x] 2.3 `RunWith(Options)`; `Run` keeps its signature and skips the `~/.claude` integration checks
- [x] 2.4 CLI: `--quick` flag, `[skip]` rendering, integration paths from `internal/config`

## 3. Docs and specs

- [x] 3.1 README.md / README_zh-TW.md
- [x] 3.2 Spec delta for diagnostics

## 4. Verification

- [x] 4.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`
- [x] 4.2 `openspec validate doctor-quick-integration --strict` and `--all --strict`
- [x] 4.3 Real-binary run with a temporary HOME and a synthetic index, `doctor` vs `doctor --quick`

## 5. Review fixes

- [x] 5.1 Freshness measures source lag, not last ingest; lease three states; footer only for index failures
- [x] 5.2 Hook commands: recogniser-resolved binary, absolute path required, `clio.exe`; MCP three states
- [x] 5.3 Windows: exec bit skipped, per-OS `pidAlive`, `GOOS=windows go vet ./...`
- [x] 5.4 Quick: `[skip] db integrity`, no quick_check, content-check skip on every path; per-check path resolution
- [x] 5.5 Tests: heavy-scan recorder, read-only HOME snapshot, `IsHeld` via `Inspect`

## 6. Round three

- [x] 6.1 Freshness judges `reconcile`'s oldest unindexed change (truncation, rewrite, complete unindexed line) instead of re-walking the files; a partial last line is not a change; wording aligned with reconciliation
- [x] 6.2 Hook check: a legacy unquoted path with whitespace warns (`unquoted path with whitespace; rerun clio install-hook`)
- [x] 6.3 Hook check: recall and file-history reported separately; a missing one and the older `Read` matcher (install-hook's `isExactReadMatcher`) warn
- [x] 6.4 MCP: a non-absolute command passes with a "resolved on doctor's PATH, not Claude Code's" note
- [x] 6.5 Lock: EPERM `pidAlive` test moved to a `!windows` file
- [x] 6.6 `--quick` scan-free proven at the SQL level (tracing driver on both connections), replacing the recorder seam; negative control with a direct `PRAGMA quick_check`
- [x] 6.7 README / flag help describe what `--quick` really skips; quick and full runs list the same row names
- [x] 6.8 Cleanup: `binaryProblem` once per hook, `clioBinaryOf` breaks out of its loop, `Run` comments no longer claim HOME independence, claudeconfig and lock inspect tests snapshot the whole directory
- [x] 6.9 Spec delta: MODIFIED "Health diagnostics" keeps the main spec's "World-readable private file flagged" scenario; ADDED requirements split one per check so each stays under the `--strict` length limit
