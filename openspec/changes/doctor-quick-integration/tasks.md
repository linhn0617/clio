## 1. Read-only helpers

- [x] 1.1 Tests first, then `lock.Inspect` (absent / malformed / fresh / stale, never writes)
- [x] 1.2 Tests first, then `claudeconfig.ClioHookCommands` and `ServerCommand`

## 2. Doctor

- [x] 2.1 Tests first: `--quick` skips the three heavy checks, runs `quick_check`, needs no writable connection
- [x] 2.2 Tests first: hook binary / mcp registration / mcp lease / index freshness against temp files
- [x] 2.3 `RunWith(Options)`; `Run` keeps its signature and stays HOME-independent
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
