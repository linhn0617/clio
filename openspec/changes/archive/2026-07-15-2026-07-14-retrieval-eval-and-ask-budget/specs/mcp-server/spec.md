## ADDED Requirements

### Requirement: The ask tool exposes a token budget parameter

The read-only `ask` MCP tool SHALL accept an optional `max_tokens` number parameter that
bounds the total estimated token size of the returned evidence bundle's excerpt text,
subject to the `ask` capability's keep-top-hits invariant (the top group's
minimum-length hit excerpts may exceed a very small budget). It SHALL default to 2000
and be clamped to a safe range (minimum 200, maximum 8000). When omitted, the default
SHALL apply. The parameter SHALL map to the `ask` capability's global token budget; all
other `ask` tool parameters SHALL be unchanged.

#### Scenario: max_tokens bounds the returned bundle

- **WHEN** an MCP `ask` call sets `max_tokens` to 500
- **THEN** the returned bundle's estimated token size SHALL NOT exceed the larger of
  500 and the top group's minimum-length hit excerpts, while still leading with the
  most relevant session's hits

#### Scenario: Omitting max_tokens applies the default

- **WHEN** an MCP `ask` call does not set `max_tokens`
- **THEN** the default budget (2000 tokens) SHALL be applied

#### Scenario: Out-of-range max_tokens is clamped

- **WHEN** an MCP `ask` call sets `max_tokens` above the maximum or below the minimum
- **THEN** the value SHALL be clamped into the allowed range rather than rejected
