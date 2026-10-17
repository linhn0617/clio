## ADDED Requirements

### Requirement: README documents transcript retention

`README.md` and `README_zh-TW.md` SHALL each explain, with the same meaning, that Claude
Code's `cleanupPeriodDays` (default 30) deletes session transcripts; that clio keeps the
indexed copy after the transcript is deleted; that raising `cleanupPeriodDays` in
`~/.claude/settings.json` keeps the original transcripts; that `clio index --purge-missing`
deletes retained sessions; and that `clio doctor` reports the retained count.

#### Scenario: Reader learns why old history survives

- **WHEN** a user reads the indexing section of either README
- **THEN** it SHALL name `cleanupPeriodDays` and its default of 30, state that clio keeps the
  indexed copy, and name `clio index --purge-missing` and `clio doctor`
