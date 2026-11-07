## ADDED Requirements

### Requirement: doctor reports the raw retention setting
`clio doctor` SHALL print a `raw retention` line directly after the `pruned raw_json`
line: the stored window (for example `14d`), or `off` when unset or when the database
has no settings table yet. A stored value that does not parse SHALL be a warning that
names the value and the fix (`clio config raw-retention off` or a valid window).

#### Scenario: Retention set
- **WHEN** retention is `14d` and `clio doctor` runs
- **THEN** the `raw retention` line reads `14d`

#### Scenario: Invalid stored value
- **WHEN** the stored value is `abc` and `clio doctor` runs
- **THEN** the `raw retention` line is a warning naming `abc`
