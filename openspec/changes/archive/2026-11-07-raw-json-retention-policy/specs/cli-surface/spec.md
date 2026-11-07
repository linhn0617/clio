## MODIFIED Requirements

### Requirement: show degrades honestly on pruned sessions
`clio show --format raw` and `--format json` SHALL detect messages whose
`raw_json` is the empty pruned sentinel and render an honest signal rather than
empty lines: `--format raw` emits a per-session note that the raw form was
pruned and can be restored with `clio index --full` (and still prints any
non-pruned lines); when a raw retention window is set, the note SHALL name the window
and give the restore path `clio config raw-retention off`, then `clio index --full`;
`--format json` emits `"RawJSON": null` for pruned messages (the pre-existing capitalized message key is preserved for backward compatibility)
and a top-level `"raw_pruned": true` flag. `--format markdown` is unaffected.

#### Scenario: Raw format notes a pruned session
- **WHEN** `clio show <uuid> --format raw` targets a fully pruned session
- **THEN** the output states the raw form was pruned and names the restore
  command, rather than printing blank lines

#### Scenario: Raw format note with retention set
- **WHEN** retention is `14d` and `clio show <uuid> --format raw` targets a pruned
  session
- **THEN** the note names `14d` and `clio config raw-retention off`

#### Scenario: JSON format flags pruned messages
- **WHEN** `clio show <uuid> --format json` targets a session with pruned
  messages
- **THEN** those messages carry `RawJSON: null` (existing key preserved) and the payload carries
  `raw_pruned: true`

#### Scenario: Markdown is unaffected
- **WHEN** `clio show <uuid>` (markdown) targets a pruned session
- **THEN** the output is identical to before pruning (markdown never used
  `raw_json`)
