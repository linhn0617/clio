## 1. Annotate the four tools

- [x] 1.1 In `internal/mcp/server.go`, attach `WithReadOnlyHintAnnotation(true)`,
  `WithDestructiveHintAnnotation(false)`, `WithIdempotentHintAnnotation(true)`,
  and `WithOpenWorldHintAnnotation(false)` to each of `search`,
  `list_sessions`, `activity_summary`, and `read_session`. Add a short comment
  explaining the worst-case default the annotations override.

## 2. Verify

- [x] 2.1 `go build ./...`, `go vet ./...`, `go test ./...` green.
- [x] 2.2 No change in tool inputs, outputs, or handler behavior — the diff
  touches only `NewTool` option lists in one file.
