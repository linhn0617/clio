## 1. Code removal

- [x] 1.1 Delete `internal/ingest/gemini.go`, `gemini_test.go`, `testdata/gemini/`
- [x] 1.2 Remove the Gemini registration (`addGeminiSource`), `geminiUsageCanon`,
      `config.GeminiTmpDir`, `model.SourceGemini` and the registry seed entry
- [x] 1.3 Re-home the generic `errUnusableStateRecord` / `unusableStateCounter` into `ingest.go`
- [x] 1.4 Strip Gemini cases from cli/mcp/registry/config/ingest tests

## 2. Data

- [x] 2.1 Migration `0014_drop_gemini.sql` (sessions, messages + FTS, tool_calls, tool_targets,
      session_usage, quota_snapshots, ingest_state, source_conflicts)
- [x] 2.2 Migration test seeding gemini and claude-code rows, asserting FTS consistency

## 3. Verification

- [x] 3.1 `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test -count=1 ./...`
- [x] 3.2 `openspec validate --all --strict`
