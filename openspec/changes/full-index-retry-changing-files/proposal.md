## Why

`clio index --full` re-ingests every file from scratch, but a file that grows while it is
being parsed fails the commit re-validation (`errStaleSnapshot`) and is silently counted
as skipped. An ordinary pass shrugs this off because the next pass catches up, but a
full re-ingest is lost: the next incremental pass resumes from the old offset, so rows
written by an older clio stay. On 2026-10-20 the maintainer's live 22 MB home-directory
session hit this during the v0.22.0 reindex and kept 39 user rows carrying
`<task-notification>` text, and the summary said only `1 skipped`.

## What Changes

- **session-ingest**: during a forced (full) ingest, a file whose commit is aborted
  because it changed on disk is retried: clio waits for the file to stay unchanged for
  one second, then re-reads and re-ingests it from scratch, up to three retries. A file
  that does not go quiet within ten seconds is given up on without another re-read.
  Each retry is a complete, independent attempt, so the commit re-validation still
  guards every write. Cancelling the run while it waits ends the ingest with the
  cancellation error.
- A file that is still changing after the last retry is reported by path in the
  ingest stats, and `clio index --full` prints each such path with a hint to rerun once
  the session is idle and Claude Code is closed (a running `clio mcp` holds the index
  lock, which `--full` refuses to run under). It still counts as skipped, and the
  command still exits 0: the file is fine, only busy. A file removed while clio waits,
  or during the last retry, is skipped as before and not listed.
- **raw-json-retention**: a session pruned by `clio prune-raw` whose file was still
  changing is no longer reported as unrestored by `clio index --full` (which would exit
  1); its file is named as still changing instead, and the next full reindex restores it.
- Unchanged: incremental passes (`clio index`, the watcher, MCP catch-up) do not retry,
  since their next pass resumes from the stored offset; a single `IngestFile` call still
  makes one attempt and aborts cleanly on a stale snapshot.

## Not done

- Committing a snapshot of a file that only grew (append) while it was read. That would
  remove the race rather than retry around it, but it relaxes the commit re-validation,
  which also guards against a second clio writer reverting the index below disk.
