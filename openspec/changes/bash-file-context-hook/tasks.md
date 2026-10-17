## 1. Hook

- [x] 1.1 Tests first: token parsing table, Bash end-to-end (history, zero candidates without
      touching the index, multi-file segments, staleness gate, output cap)
- [x] 1.2 `bashFileCandidates` / `splitShellWords`, Bash branch and multi-segment digest in
      `fileHistoryHook` (single pointer line, shrink loop over all segments)

## 2. Installer

- [x] 2.1 Tests first: fresh install, `Read` clio-only upgrade, mixed-group move, idempotence,
      both matchers detected, user matcher untouched, uninstall of `Read|Bash`
- [x] 2.2 Matcher `Read|Bash`, `upgradeReadFileHistory`, `HasLegacyPreToolUseReadHook`,
      install-hook upgrade message and `PreToolUse(Read|Bash)` wording

## 3. Docs and specs

- [x] 3.1 README.md / README_zh-TW.md
- [x] 3.2 Spec deltas for file-history and recall-hook

## 4. Verification

- [x] 4.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`
- [x] 4.2 `openspec validate bash-file-context-hook --strict` and `--all --strict`
- [x] 4.3 Real-binary run against an empty XDG data dir (Bash with and without a file candidate)

## 5. Review fixes

- [x] 5.1 Tokenizer: heredoc bodies, comments, redirect targets, `cd`/`pushd`, 64-word cap,
      symlink-resolved boundary, shared `internal/shellwords`
- [x] 5.2 Installer: Read-covering matchers count as installed, duplicate collapse, upgrade
      message only on a real retarget
- [x] 5.3 Hook: single read-only open (injectable), sequential budget, stat kept from selection
