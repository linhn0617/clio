## 1. Spec

- [x] 1.1 Change validates with `openspec validate raw-json-retention-policy --strict`.

## 2. Setting

- [x] 2.1 Migration 0017 `settings` table; `ParseRetention`; `DB.Setting/PutSetting/DeleteSetting/RawRetention`.
- [x] 2.2 `clio config raw-retention [VALUE|off]`, registered on the root command.

## 3. Ingest

- [x] 3.1 Write-time drop for rows each file commit inserts; activity facts from parsed events (pinned by a test with `RepairActivity`).
- [x] 3.2 Invalid or unreadable setting: raw kept, warning naming the value.

## 4. clio index

- [x] 4.1 Retention sweep through prune-raw's shared preflight in `clio index` and once at MCP leader startup; per-read catch-ups do not sweep.
- [x] 4.2 Residual check excludes retention-blanked sessions; help text states the restore path.

## 5. Visibility and docs

- [x] 5.1 `clio doctor` `raw retention` line; `show --format raw` note names the window.
- [x] 5.2 READMEs (en, zh-TW) and `docs/USAGE.md`.

## 6. Final verification fixes

- [x] 6.1 `prune-raw --vacuum` VACUUMs even when nothing was left to prune.
- [x] 6.2 prune-raw's closing restore hint turns retention off first when a window is set.
- [x] 6.3 `config raw-retention` confirmation and `docs/USAGE.md` say the sweep runs at the next MCP leader start while `clio mcp` is running.
- [x] 6.4 `index --full` keeps the "raw retention sweep failed" prefix when the residual check fails too.
- [x] 6.5 Incremental `upsertSession` keeps `ended_at` when the batch has no timestamp (EndedAt 0 / NULL).
