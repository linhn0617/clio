# cli-surface (delta)

## ADDED Requirements

### Requirement: show degrades honestly on pruned sessions
`clio show --format raw` and `--format json` SHALL detect messages whose
`raw_json` is the empty pruned sentinel and render an honest signal rather than
empty lines: `--format raw` emits a per-session note that the raw form was
pruned and can be restored with `clio index --full` (and still prints any
non-pruned lines); `--format json` emits `"raw_json": null` for pruned messages
and a top-level `"raw_pruned": true` flag. `--format markdown` is unaffected.

#### Scenario: Raw format notes a pruned session
- **WHEN** `clio show <uuid> --format raw` targets a fully pruned session
- **THEN** the output states the raw form was pruned and names the restore
  command, rather than printing blank lines

#### Scenario: JSON format flags pruned messages
- **WHEN** `clio show <uuid> --format json` targets a session with pruned
  messages
- **THEN** those messages carry `raw_json: null` and the payload carries
  `raw_pruned: true`

#### Scenario: Markdown is unaffected
- **WHEN** `clio show <uuid>` (markdown) targets a pruned session
- **THEN** the output is identical to before pruning (markdown never used
  `raw_json`)
