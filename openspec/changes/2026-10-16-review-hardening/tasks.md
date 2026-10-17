## 1. Retain history when the source transcript disappears (A1)
- [x] 1.1 Migration `0013_source_missing.sql`: `ingest_state.source_missing_at INTEGER`
  (NULL = file present).
- [x] 1.2 `PurgeMissing` marks instead of deleting (existing mark kept; reappeared file
  unmarked; moved file's stale state dropped; no mass-disappearance cap on marking) (TDD:
  `TestPurgeMissingMarksInsteadOfDeleting`, `…ClearsMarkWhenFileReappears`,
  `…MarksEvenOnMassDisappearance`, `…KeepsMovedSession`).
- [x] 1.3 `DeleteMissing` = mark + purge still-missing, cap kept and surfaced as
  `ErrPurgeRefused` (TDD: `TestDeleteMissingPurgesOnlyStillMissing`,
  `…AbortsOnMassDisappearance`; former purge tests moved onto `DeleteMissing`).
- [x] 1.4 `upsertIngestState` clears `source_missing_at` on every successful commit.
- [x] 1.5 `index --full` keeps retained sessions and all their rows (TDD:
  `TestFullReindexKeepsRetainedSession`; the full path only rewrites walked files, so no
  code change was needed — the test pins it).
- [x] 1.6 `clio index --purge-missing`, refused under the MCP lock like `--full` (TDD:
  `TestIndexPurgeMissingDropsRetainedSession`, `…RefusedUnderLock`).
- [x] 1.7 doctor: reconcile counts marked rows as retained, coverage excludes them, new
  `retained sessions` line (0 printed too), pre-0013 read-only DB tolerated (TDD:
  `TestReconcileCountsRetainedNotMissing`, `TestCoverageBySourceExcludesRetained`,
  `TestRunReportsRetainedSessions`, `TestRunToleratesMissingSourceMissingColumn`).
- [x] 1.8 prune-raw never blanks a retained session (already excluded by its stat check;
  TDD pin: `TestPruneRawSkipsRetainedSession`).
- [x] 1.9 Watcher backstop test now asserts retention (`TestWatcherBackstopRetainsDeleted`).
- [x] 1.10 README.md / README_zh-TW.md "When Claude Code deletes old transcripts" section;
  `docs/USAGE.md` index flag table row.
- [x] 1.11 Delta specs: session-ingest, file-watcher, usage-summary, diagnostics,
  cli-surface, documentation.
- [x] 1.12 `DeleteMissing` keeps a session whose uuid still has another tracked source path
  that exists on disk (a full reindex can leave two `ingest_state` rows for one uuid while
  `sessions.source_file` names the deleted one); it logs and skips (codex #1; TDD:
  `TestDeleteMissingKeepsSessionWithLiveDuplicatePath`, red first).
- [x] 1.13 Purge cap counts only the largest cluster of sources marked missing within 24h
  (inclusive) of the cluster's earliest mark, so Claude Code's batched cleanup no longer
  accumulates into a permanent refusal; `clio index --purge-missing --force` skips the cap,
  `--force` alone is an error; refusal message names the cluster and `--force`; README×2,
  `docs/USAGE.md` and the cli-surface / file-watcher deltas updated (TDD:
  `TestDeleteMissingAllowsAccumulatedBatches`, `TestDeleteMissingRefusesClusterUnlessForced`,
  `TestDeleteMissingClusterSplitByTime`, `TestIndexPurgeMissingForceOverridesCap`,
  `TestIndexForceRequiresPurgeMissing`; red first).
- [x] 1.14 Second-review fixes: the live-copy lookup matches the uuid in the file name only (a
  subagent file under `<parent uuid>/subagents/` no longer shields a deleted parent), and a
  live copy is closed as a move (`sessions.source_file` repointed, the old `ingest_state` row
  dropped, so it is not reported as retained) instead of the 1.12 skip; a failed lookup is an
  error and that source is not purged; the marking loop runs in one transaction so a killed
  pass cannot split one disappearance into two clusters; the 24h window is inclusive (TDD:
  `TestDeleteMissingSubagentIsNotParentCopy`, extended
  `TestDeleteMissingKeepsSessionWithLiveDuplicatePath`, `TestLiveDuplicateSourceReportsQueryError`,
  `TestDeleteMissingClusterWindowInclusive`; the transaction is covered by the existing suite).

## 2. FTS optimize (A2)
- [x] 2.1 `optimizeFTS` helper; `index --full` runs FTS5 `optimize` after IngestAll and
  `--purge-missing`, warn-only on failure, silent for incremental (TDD:
  `TestOptimizeFTSShrinksSegments`, `TestIndexFullOptimizesFTS`,
  `TestIncrementalIndexDoesNotOptimize`; mutation-checked by swapping the command).
- [x] 2.2 `prune-raw --vacuum` optimizes before VACUUM, warn-only (TDD:
  `TestPruneVacuumOptimizesFTS`).
- [x] 2.3 doctor `fts bloat` line via dbstat, threshold ratio 2.0 with a 16 MB floor,
  unavailable => ok (TDD: `TestDbstatAvailableForFTSBloat`, `TestFTSBloatVerdict`,
  `TestRunReportsFTSBloat`, `TestFTSBloatUnavailableIsNotRed`).
- [x] 2.4 Delta specs: fts-search, diagnostics, raw-json-retention.

## 3. JSON key redaction (A3)
- [x] 3.1 `redactWalkDepth` map branch redacts keys through `redactString` (fresh map, sorted
  keys; `isSecretKey` still judged on the original key; colliding redacted keys collapse to
  `[REDACTED:key]`) (TDD: `TestRedactJSONKeys`, red against HEAD then green).
- [x] 3.2 Delta spec: session-ingest.

## 4. `list` hides subagents whose parent is indexed (A4)
- [x] 4.1 `ListSessions` hides a child whenever its parent is indexed and passes the same
  filters, no LIMIT in the parent subquery; comments in `ListSessions`/`listFilters`/`GetRecall`
  updated (TDD: `TestListSessionsHidesChildWhenParentBeyondPage`, red then green; the
  superseded `TestListSessionsPromotesRecentChildBeyondParentPage` was inverted into it;
  filter-excluded and orphan promotion tests unchanged and green).
- [x] 4.2 Delta specs: cli-surface (MODIFIED), recall-hook (MODIFIED, parenthetical only).

## 5. recall digest fixes (A5)
- [x] 5.1 Exclude the hook's own session: `recall` reads `session_id` from the SessionStart
  payload; `ListFilter.ExcludeSession` and the recall activity query drop it and its
  subagents from the excerpt, sessions, files, and commands (fallback path too); manual
  run unchanged (TDD: `TestGetRecallExcludesCurrentSession`,
  `TestRecallDigestExcludesHookSession`, `TestParseHookPayloadSessionID`).
- [x] 5.2 `/` path boundary for the project filter in `ListFilter.ProjectPrefix`,
  `ActivityByKind`, `ActivitySummary` (`pathUnderClause`; trailing `/` ignored); flag text of
  `list`/`activity`/`recall --project` and MCP `list_sessions`/`activity_summary` updated
  (TDD: `TestListSessionsProjectPathBoundary`, `TestActivityByKindProjectPathBoundary`,
  `TestActivitySummaryProjectPathBoundary`).
- [x] 5.3 Recall's touched files limited to paths under the project root, filtered in SQL
  before LIMIT; commands unchanged (TDD: `TestGetRecallFilesUnderProjectOnly`).
- [x] 5.4 Digest capped at 9,500 runes (`recallMaxChars`); shrink order commands → files →
  one session → excerpt 200 runes → hard cut with `…` (TDD: `TestFormatRecallStaysUnderCap`,
  `TestFormatRecallShrinkOrder`, `TestFormatRecallHugeExcerpt`, `TestFormatRecallHardCut`;
  mutation-checked: cap off, exclusion off, excerpt cut off, step order swapped, hard cut off
  each turn a test red).
- [x] 5.5 `install-hook` writes the recall hook with matcher `startup|resume|clear|compact`
  and `timeout: 10`; an unmatched (legacy) recall entry is upgraded in place, a co-grouped
  one moves to its own group, a user-chosen matcher is left alone; uninstall handles both
  shapes; install reports the upgrade (TDD: `TestAddSessionStartHookWritesMatcherAndTimeout`,
  `…UpgradesLegacyInPlace`, `…UpgradesCoGroupedLegacy`, `…KeepsCustomMatcher`,
  `TestRemoveClioHooksRemovesBothRecallShapes`, `TestInstallHookReportsRecallUpgrade`,
  `TestInstallHookUpgradesRecallOnlyConfig` extended).
- [x] 5.6 Delta specs: recall-hook (MODIFIED digest + install), cli-surface (MODIFIED
  activity filters).
- [x] 5.7 `pathUnderClause` compares with `substr`/`length` instead of LIKE, so the
  boundary is case-sensitive and needs no escaping (LIKE folds ASCII case: `/x/foo` matched
  `/x/FOO/sub`); the root `/` branch likewise (codex #5; TDD:
  `TestListSessionsProjectPathCaseSensitive`, red first).

## 6. Terminal escape stripping (A6)
- [x] 6.1 New `internal/termsafe` with `Strip` (CSI/OSC/DCS/APC/PM/SOS/two-char ESC, C0 except
  `\n` `\t`, DEL, C1; unterminated sequences dropped; `\r` removed so a line cannot overwrite
  the previous one) (TDD: `TestStrip` table).
- [x] 6.2 CLI human-readable output stripped at every indexed-text print site: `oneLine`,
  `trimProject`, `show` markdown (title, project, content, agent types), search snippet and
  agent type, `activity` value, `recall` (project, excerpt, files), `file-history`
  (path, tools), `usage` agent type and group key (TDD: `TestShowMarkdownStripsTerminalEscapes`,
  `TestFormatRecallStripsTerminalEscapes`, `TestSearchAndListLinesStripTerminalEscapes`,
  `TestWriteAnswerStripsTerminalEscapes`, `TestFormatFileHistoryStripsTerminalEscapes`,
  `TestFormatActivityRowStripsTerminalEscapes`; red before the change). `show` markdown
  and the activity row were extracted into `writeShowMarkdown` / `formatActivityRow` to be testable.
- [x] 6.3 TUI: `oneLine`, preview content, Ask excerpts, subagent type tags (TDD:
  `TestOneLineStripsTerminalEscapes`, `TestRenderPreviewStripsTerminalEscapes`,
  `TestAskExcerptsStripTerminalEscapes`).
- [x] 6.4 JSON, raw, hook JSON, and MCP output untouched.
- [x] 6.5 Delta specs: cli-surface (ADDED), tui (ADDED).
- [x] 6.6 `usage --quota` strips `limit_id` and `plan_type` (codex #2; TDD:
  `TestUsageCmdQuotaStripsTerminalEscapes`, red first).
- [x] 6.7 `termsafe` C1 handling completed: a raw C1 byte outside valid UTF-8 is stripped, and
  C1 CSI/OSC/DCS/SOS/PM/APC introducers and ST (UTF-8 or raw) act like their ESC forms;
  control strings step by rune so a UTF-8 continuation byte is never read as ST (codex #4;
  TDD: new `TestStrip` rows, red first; the old `\u009b` row expected `a31mb` and now
  expects `ab`).
- [x] 6.8 Unterminated-sequence wording in the `termsafe` doc comment and the cli-surface / tui
  deltas states that a new ESC aborts the earlier sequence (codex #6; behaviour unchanged).

## 7. install-hook quoting + symlink (A7)
- [x] 7.1 `claudeconfig.ShellQuote` (POSIX single quotes, `'\''` for an embedded quote; plain
  paths returned unchanged) and a minimal `splitCommand` tokenizer behind `isClioRecall` /
  `isClioFileHistory`, so quoted and bare first words are both recognised (TDD:
  `TestShellQuote` incl. round trip, `TestIsClioHookCommandsQuotedPaths`,
  `TestInstallQuotedPathRegistersOnceAndUninstalls`, `TestInstallHookQuotesExecutablePathWithSpaces`;
  red first). `install-hook` quotes the exe for both commands; the `// debt:` note is gone.
  `install-mcp` writes the exe into JSON `command` (an argv field, not a shell string), so it
  needs no quoting and is unchanged.
- [x] 7.2 `mutate` resolves a symlinked config (`resolveConfigPath`) and locks, writes the
  temp file, renames, and writes `.bak` at the real target; a missing path is used as is; a
  dangling symlink is an error (TDD: `TestMutateFollowsSymlink`,
  `TestMutateRefusesDanglingSymlink`; red first).
- [x] 7.3 Delta specs: recall-hook (MODIFIED install requirement, quoting sentence +
  scenario), mcp-installer (MODIFIED Safe config mutation, symlink scenarios).
- [x] 7.4 The unquoted space-path shape an older install wrote (`/My Tools/clio recall`) is
  recognised as clio's (bare absolute path, no quote or metacharacter, basename `clio`):
  install rewrites it in place to the quoted current command, uninstall removes it; the
  legacy-matcher in-place upgrade now also writes the current binary path (TDD:
  `TestInstallUpgradesUnquotedSpacePathAndUninstalls`, `TestRemoveClioHooksRemovesUnquotedSpacePath`,
  `TestInstallHookRepairsUnquotedSpacePath`, extended `TestIsClioHookCommandsQuotedPaths` and
  `TestAddSessionStartHookUpgradesLegacyInPlace`; red first, the install-hook test by a negative control). Delta: recall-hook. install-hook reports the repair as an upgrade (TDD: `TestInstallHookReportsUnquotedRepair`, red first).
- [x] 7.5 The unquoted-shape recogniser rejects wrappers: a piece after the first that starts with
  `/` or `~` (`/usr/bin/env /opt/clio recall`, `/usr/bin/sudo /opt/clio file-history --hook`)
  marks a foreign command, which install leaves as is and uninstall keeps (TDD:
  `TestWrapperCommandsAreNotClioHooks`, extended `TestIsClioHookCommandsQuotedPaths`; red first).
