## 1. Red-first tests

- [x] 1.1 Full ingest of a file that grows once before commit: retried and ingested,
      grown content stored, not counted as skipped.
- [x] 1.2 File that changes before every commit: skipped after three retries, path
      listed, prior rows and `ingest_state` untouched.
- [x] 1.3 Non-forced pass: no retry, behaves as before.
- [x] 1.4 `printIndexSummary` prints each still-changing path to stderr; `clio index
      --full` prints its summary through it (the command cannot be made to see a
      changing file, so the path list itself is tested on the helper).
- [x] 1.5 A file long enough to resume from its offset is still rebuilt from scratch on
      retry; a file removed while waiting is skipped, not listed; a cancelled wait
      returns at once (added after the first fresh verification found these untested).

## 2. Implementation

- [x] 2.1 Split `IngestFile` into a single attempt that reports a stale abort; keep
      `IngestFile`'s contract.
- [x] 2.2 In `IngestAll`, when forced, wait for the file to settle and retry up to three
      times; record unsettled paths in `Stats`.
- [x] 2.3 Print the paths in `clio index`.

## 2b. After code review

- [x] 2b.1 A wait that runs out without the file going quiet gives up on the file
      instead of re-reading it (`waitSettled` reports whether it settled).
- [x] 2b.2 A file removed during the last attempt is skipped, not listed as still
      changing.
- [x] 2b.3 The loop comment says "removed", not "removed or replaced"; the stderr hint
      says to rerun with Claude Code closed.
- [x] 2b.4 Red-first tests for 2b.1 and 2b.2; the quiet-wait test also checks the
      return comes at least `quiet` after the last write.

## 2c. After the second code review

- [x] 2c.1 Cancelling while waiting to retry ends `IngestAll` with the context's error
      (it was logged as a file failure and the pass reported success).
- [x] 2c.2 `clio index --full` does not fail the pruned-residual check for a session
      whose file is still changing (`checkPrunedResiduals`).
- [x] 2c.3 The quiet-wait test records the write time before the write.
- [x] 2c.4 The summary test checks the hint names Claude Code.
- [x] 2c.5 Red-first tests for 2c.1 and 2c.2; negative controls for 2c.1, 2c.2 (both
      directions) and 2c.4, each red, each reverted.
- [x] 2c.6 raw-json-retention: MODIFIED delta for the still-changing exception to the
      unrestored-prune exit (found by the fresh verification).

## 3. Verification

- [x] 3.1 Negative controls (no retry, retry without waiting for quiet, retry on
      incremental passes, path not recorded, no quiet window, no wait cap), each
      reverted; every one red on its own test. After verification: retry with
      force=false, drop the summary call, ignore cancel while waiting, and count a
      removed file as an error, each red. Added a direct test of the wait
      (TestFullIngestWaitsBeforeRetrying) after "retry without waiting" first stayed
      green.
- [x] 3.2 gofmt, go vet, all tests with -race on Go 1.26 and 1.27.
- [x] 3.3 CHANGELOG entry.
- [x] 3.4 openspec validate full-index-retry-changing-files --strict.
