## MODIFIED Requirements

### Requirement: Single-writer ownership

The system SHALL act as the sole writer while the MCP server runs.

At startup the leader SHALL run exactly one catch-up (ingest, activity backfill and deletion
reconciliation) and SHALL NOT run a separate synchronous ingest before the MCP server starts
serving; the server SHALL accept requests immediately. The leader SHALL begin renewing its
lease before that catch-up starts and keep renewing for as long as it is leader, so a catch-up
longer than the lease TTL does not forfeit leadership. If renewal reports that another process
holds the lease, any catch-up in progress SHALL be cancelled and the process SHALL demote to
follower. The process SHALL be treated as leader for read purposes only once the catch-up has
completed (a process SHALL start with that state unset, even when it holds the lease); reads arriving earlier SHALL use the follower read catch-up, which SHALL NOT run
concurrently with the leader's catch-up, nor start a rebuild of its own before the leader's catch-up has begun. The watcher's periodic reconciliation walk SHALL NOT run concurrently with such a walk on the same ingester, and on shutdown or demotion the catch-up, watcher and renewal SHALL have stopped before the lease is released.

#### Scenario: Lock file written on start

- **WHEN** `clio mcp` starts
- **THEN** the system SHALL write a lock file containing its pid so CLI invocations defer to read-only

#### Scenario: Lease renewed during a long startup catch-up

- **WHEN** the leader's startup catch-up runs longer than the lease TTL
- **THEN** the lease heartbeat SHALL keep being refreshed during the catch-up

#### Scenario: Superseded during startup catch-up

- **WHEN** lease renewal finds another process owns the lease while the catch-up runs
- **THEN** the catch-up SHALL be cancelled and the process SHALL demote to follower

#### Scenario: Early read during startup catch-up

- **WHEN** an MCP read arrives while the leader's catch-up is still running
- **THEN** it SHALL be served without waiting for the catch-up and without starting a second concurrent ingest

#### Scenario: Cancelled catch-up does not continue

- **WHEN** the startup catch-up is cancelled (superseded or shutdown) between phases
- **THEN** later phases (activity backfill, deletion reconciliation) SHALL NOT run and the process SHALL NOT be marked leader
