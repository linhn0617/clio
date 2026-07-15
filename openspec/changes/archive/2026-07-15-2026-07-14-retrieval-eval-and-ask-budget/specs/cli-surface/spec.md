## ADDED Requirements

### Requirement: clio ask exposes a token budget flag

`clio ask` SHALL accept a `--max-tokens` integer flag that bounds the total estimated
token size of the evidence bundle's excerpt text, mapping to the `ask` capability's
global token budget (and subject to its keep-top-hits invariant). When the flag is
omitted (or 0), the package default (2000 tokens) SHALL apply. The flag SHALL affect
both the human-readable and `--json` output.

#### Scenario: --max-tokens bounds the bundle

- **WHEN** `clio ask "<question>" --max-tokens 500 --json` runs against a large history
- **THEN** the emitted bundle's estimated token size SHALL NOT exceed the larger of 500
  and the top group's minimum-length hit excerpts, still leading with the most relevant
  session's hits

#### Scenario: Omitting the flag applies the default budget

- **WHEN** `clio ask "<question>"` runs without `--max-tokens`
- **THEN** the default budget (2000 tokens) SHALL be applied
