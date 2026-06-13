## 1. Recall digest (read layer + command)

- [x] 1.1 Read helper (TDD): project-scoped recall data — recent sessions
  (`ListSessions` by project), recently touched files and recently run commands
  (`ActivityByKind` with project + since). Reuse existing queries.
- [x] 1.2 `clio recall` command (TDD): detect the project from the working
  directory (SessionStart stdin `cwd`, fallback `os.Getwd()`), with
  `--project`/`--since`/`--limit`/`--no-commands` overrides; print a concise digest;
  empty output when the project has no history; read-only connection; exit 0 on any
  error.

## 2. Hook installer

- [x] 2.1 Atomic `~/.claude/settings.json` editor (TDD): add/remove a SessionStart
  hook that runs `<clio absolute path> recall`, removing only clio's entry
  (preserving co-grouped hooks), atomic (original intact on failure), fail-safe on
  a non-object/missing config.
- [x] 2.2 `clio install-hook` / `clio uninstall-hook` commands wired to it.

## 3. Verify

- [x] 3.1 `go build/vet/test ./...` green; `openspec validate --strict`.
- [x] 3.2 `clio recall` is read-only and silent on error (missing/unreadable DB →
  exit 0, empty output). Test.
- [x] 3.3 `install-hook`/`uninstall-hook` preserve existing SessionStart hooks
  (including co-grouped ones); the clio-recall matcher does not false-match. Test.
- [x] 3.4 Third-party (codex) review of the diff (round 1: P1 group-removal + a P2
  subdir-scoping + a P2 doc claim; all fixed; round 2 gate PASS).
