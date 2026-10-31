## ADDED Requirements

### Requirement: A full re-ingest retries files that change while being read

The system SHALL retry a file during a forced (full) ingest when its commit is aborted
because the file changed on disk since it was read: it SHALL wait until the file has
stayed unchanged for one second and then re-ingest the file from scratch, up to three
retries. Every retry SHALL go through the same commit re-validation. A file that does not
stay unchanged for a second within ten seconds of waiting, or that is still changing
after the last retry, SHALL be counted as skipped and reported by path without a further
re-read. Incremental passes SHALL NOT retry.

#### Scenario: Live file settles

- **WHEN** a full ingest reads a file, the file grows before the commit, and then stops
  growing
- **THEN** the system SHALL re-ingest it from scratch and commit the grown file, and the
  file SHALL count as ingested, not skipped

#### Scenario: File never settles

- **WHEN** the file changes before every commit, including the last retry's
- **THEN** the system SHALL leave its `ingest_state` and rows as they were, count it as
  skipped, and list its path among the files still changing

#### Scenario: File keeps changing through the wait

- **WHEN** the file is still changing when ten seconds of waiting have passed
- **THEN** the system SHALL NOT re-read it, and SHALL count it as skipped and list its
  path among the files still changing

#### Scenario: File removed while waiting

- **WHEN** the file is removed or can no longer be stat'd while the system waits to retry
  it, or is removed during the last retry
- **THEN** the system SHALL count it as skipped, as a single attempt would, and SHALL NOT
  list it among the files still changing

#### Scenario: Retry rebuilds from scratch

- **WHEN** a retried file is long enough that an incremental pass could resume from its
  stored offset
- **THEN** the retry SHALL still re-ingest it from scratch, replacing every row an earlier
  ingest wrote for it

#### Scenario: Summary names the files

- **WHEN** `clio index --full` finishes with files still changing
- **THEN** it SHALL print each such path to stderr with a hint to rerun once the session
  is idle and Claude Code is closed, and SHALL still exit successfully, including when
  such a file belongs to a session pruned by `clio prune-raw` (it is restored by the
  next full reindex, so it is not reported as unrestorable)

#### Scenario: Cancelled while waiting

- **WHEN** the run is cancelled while the system waits to retry a file
- **THEN** the forced ingest SHALL stop and return the cancellation as an error, not
  report the pass as finished

#### Scenario: Incremental pass

- **WHEN** a non-forced pass hits a file that changed before its commit
- **THEN** the system SHALL abort that file's commit without retrying, as before
