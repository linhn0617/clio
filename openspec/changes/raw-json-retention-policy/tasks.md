## 1. Spec

- [x] 1.1 Change validates with `openspec validate raw-json-retention-policy --strict`.

## 2. Setting

- [x] 2.1 Migration 0017 `settings` table; `ParseRetention`; `DB.Setting/PutSetting/DeleteSetting/RawRetention`.
- [x] 2.2 `clio config raw-retention [VALUE|off]`, registered on the root command.

## 3. Ingest

- [ ] 3.1 Write-time drop for rows each file commit inserts; activity facts from parsed events (pinned by a test with `RepairActivity`).
- [ ] 3.2 Invalid or unreadable setting: raw kept, warning naming the value.

## 4. clio index

- [ ] 4.1 Retention sweep through prune-raw's shared preflight in `clio index` and once at MCP leader startup; per-read catch-ups do not sweep.
- [ ] 4.2 Residual check excludes retention-blanked sessions; help text states the restore path.

## 5. Visibility and docs

- [ ] 5.1 `clio doctor` `raw retention` line; `show --format raw` note names the window.
- [ ] 5.2 READMEs (en, zh-TW) and `docs/USAGE.md`.
