# diagnostics (delta)

## ADDED Requirements

### Requirement: doctor reports pruned raw_json
`clio doctor` SHALL report two well-defined figures (original raw_json lengths
are gone after pruning, so no per-raw byte estimate is fabricated): the exact
pruned-message count (`COUNT(*) WHERE raw_json=''`), and the whole-DB freelist
size (`freelist_count × page_size`) labeled as "reclaimable on VACUUM (whole-DB
freelist, not raw-only)". Informational, not a failing check.

#### Scenario: doctor surfaces pruned counts
- **WHEN** `clio doctor` runs on a database where some sessions have been pruned
- **THEN** the output includes the exact pruned-message count and the labeled
  whole-DB freelist figure

#### Scenario: doctor on an unpruned database
- **WHEN** `clio doctor` runs on a database with no pruned messages
- **THEN** the pruned-raw line reports zero (or is a clean pass), not an error
