## 1. Frame

- [x] 1.1 Bubble Tea root model (TDD): tab bar + tab switching (`Tab`/`Shift-Tab`,
  `1`–`4`), master-detail split, shared preview, status line; `Update` transitions
  tested without a real terminal.
- [x] 1.2 `clio tui` command: open the index read-only after a startup catch-up
  (like `search`); run the program; quit on `q`/`Ctrl-C`; missing index reported.

## 2. Views (each TDD: Update transitions + empty/populated states)

- [x] 2.1 Search view: debounced live `search.Search`; list + selection; preview
  the hit session via `sessions.GetMessages` with the matched line marked.
- [x] 2.2 Browse view: `sessions.ListSessions` (recent / `--project`); preview.
- [x] 2.3 Activity view: `sessions.ActivityByKind`; selecting an entry drills to its
  sessions (`Touched`/`Ran` filter).
- [x] 2.4 Ask view: question input → `ask.Ask`; render the grouped evidence bundle;
  preview the selected group's excerpts.

## 3. Cross-cutting

- [x] 3.1 Queries run as `tea.Cmd` (UI never blocks); query errors show in the
  status line and never crash; empty index prompts `clio index`.

## 4. Verify

- [x] 4.1 `go build/vet/test ./...` green (incl. `-race` + windows cross-build);
  `openspec validate --strict`.
- [x] 4.2 Third-party (codex) review of the real diff to a clean gate; then Claude
  `/review`.
