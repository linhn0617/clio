## 1. Codex activity-target extraction (`internal/ingest/codex.go`)

- [ ] 1.1 `codexCommandTarget(name, args) (kind, value string, ok bool)` helper:
  `exec_command.cmd` → command; `shell` argv script (scan for `-c`/`-lc` flag, take the
  next element, else `strings.Join`) → command; `view_image.path` → file; else `ok=false`
  (TDD, real redacted Codex fixture).
- [ ] 1.2 `codexExtractTargets(name, args) []model.ToolTarget`: mirror the shared contract —
  nil for empty name or `clioMCPToolPrefix`; otherwise `{tool, name}` first, then the
  redacted + capped domain fact from 1.1 (TDD).
- [ ] 1.3 Codex tool-use summary from the same helper: `firstLine(redactString(value), 200)`
  (redact before truncate), replacing the empty `toolUseSummary` result in the
  `function_call` branch; shared `toolUseSummary` untouched (TDD).
- [ ] 1.4 Wire both into `codexSource.ParseFile`'s `function_call` case (replace the
  `extractTargets` / `toolUseSummary` calls).

## 2. Verify

- [ ] 2.1 Unit (table-driven: exec_command, shell `bash -lc`, shell argv variants
  `/bin/bash` & split `-l -c`, no-flag join fallback, view_image, clio-MCP → nil, tool-only
  tools, apply_patch-as-command, secret redaction past byte 200) + `ParseFile` fixture +
  end-to-end into a **temp db** (temp dir / absolute `XDG_DATA_HOME` redirect, never the
  live db): `ActivityByKind(command/file, "codex")` and `list --ran/--touched --source codex`
  return Codex activity; default source unaffected; summary shows the command.
- [ ] 2.2 `go build/vet/test ./...` green (incl. `-race`); `gofmt -l .` clean;
  `openspec validate --strict`.
- [ ] 2.3 Codex review of the real diff to a clean gate (re-review after each fix); then
  Claude `/review`.
- [ ] 2.4 Docs: CHANGELOG `## [0.9.1]`; README ×2 note that `--source codex` now surfaces
  command/file activity and that `clio index --full` refreshes Codex rows indexed by v0.9.0.
