## Why

`clio mcp`'s leader startup had three defects. It ran the catch-up twice (a synchronous
`IngestAll` before `Serve`, then the same `IngestAll` again at the start of the leader loop).
It started lease renewal only after the loop's catch-up, backfill and purge finished, so any
startup work longer than the 10 second lease TTL (a full rebuild takes minutes) let another
clio process take over leadership; the 2026-10-15 mcp log shows "abandoned after 15064ms".
And `Serve` waited for the pre-Serve ingest, so the MCP client got no response for that time.

## What Changes

- **file-watcher**: the leader's startup runs a single catch-up (ingest, activity backfill,
  deletion reconciliation); the synchronous pre-Serve ingest is removed and the server starts
  answering immediately. Lease renewal starts before the catch-up and runs for the whole
  leader tenure. If renewal finds the lease superseded during the catch-up, the catch-up is
  cancelled and the process demotes to follower.
- Until the catch-up finishes the process is not yet flagged leader, so reads that arrive early
  use the existing throttled read catch-up. A mutex shared with the leader catch-up makes that
  read catch-up skip (serving possibly-stale data) rather than walk the same Ingester
  concurrently or block behind a long rebuild.
- The watcher gains an optional `BackstopGuard` so its reconciliation walk shares the same mutex. No change to the lock package or the worker pool.

## Impact

- A second `clio mcp` can no longer steal leadership during a long startup catch-up.
- MCP clients get a response at once; very early reads may see data from before the catch-up.
- Internal refactor of `internal/cli/mcp.go`: `leaderLoop`/`runLeaseRole` take a `leaderEnv`
  (ingester interface, shared mutex, renew interval, watcher starter) so tests can inject a slow catch-up.
