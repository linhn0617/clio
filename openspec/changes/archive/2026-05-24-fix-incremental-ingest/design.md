# Design — fix-incremental-ingest

Umbrella design: `docs/superpowers/specs/2026-05-24-clio-quality-security-batch-design.md` (change ②).

## Schema (migration 0005)

Add two columns to `ingest_state`:

- `head_fingerprint TEXT NOT NULL DEFAULT ''` — fingerprint of the file's first bytes.
- `unparsed_lines INTEGER NOT NULL DEFAULT 0` — running count of complete lines that
  failed to parse for this source.

`DEFAULT` values keep the migration backward-compatible with existing rows. An empty
stored `head_fingerprint` means "unknown" (eng-review D2): the resume path SKIPS the head
check and backfills the real fingerprint on that pass, so existing users do NOT get a
forced full reingest of their whole history on upgrade. The head check only applies once a
non-empty fingerprint has been stored.

## C3 — unparseable line: advance, count, surface

Decision (umbrella decision 2): a complete-but-unparseable line is almost always a
permanent condition (corruption or an event shape clio cannot parse), because partial
writes are already excluded by only processing up to the last complete newline.
Therefore:

- Keep skipping the line and advancing the watermark (liveness: one poison line must
  not wedge all later messages in the file).
- Count skipped lines per file and ADD them to `ingest_state.unparsed_lines` on each
  incremental pass; reset to the run count only on a full reingest (see semantics below).
- `clio doctor` sums `unparsed_lines` and, when non-zero, reports:
  "N source lines could not be parsed; after upgrading clio, run `clio index --full`."

This removes the *silent* part. The line is still not indexed until a parser upgrade +
full reindex, which is the correct contract for genuinely unparseable input.

## C2 — head + tail fingerprint

`internal/ingest/incremental.go` currently fingerprints the 512 bytes ending at the
stored offset. Add a head fingerprint (first 512 bytes). On incremental resume,
validate BOTH:

- tail fingerprint at the stored offset (unchanged content up to where we resume), and
- head fingerprint (first 512 bytes stable).

Mismatch on either → `changeFull` (full reingest). For append-only JSONL, the first
session line never changes, so the head fingerprint cheaply catches a file replaced
with different content — the realistic rewrite case. A mid-file edit that preserves
head, tail, and size still slips, but clio's source files are append-only and never
exhibit that; closing it fully would need a whole-prefix hash (out of scope, YAGNI).

Empty stored head fingerprint (eng-review D2): when the stored value is `''` (a row
written before migration 0005), skip the head comparison for that pass and persist the
computed head fingerprint, so the upgrade does not force a full reingest of every file.

## C4 — streaming read, atomicity preserved

Replace `readFrom` + `io.ReadAll` + in-memory `splitLines` with a streaming reader
(`bufio.Reader.ReadBytes('\n')`) seeded at the start offset, feeding the parser one
complete line at a time inside the SAME per-file transaction. Memory is bounded to one
line plus the reader buffer. The final partial line (no trailing newline) is detected
and left for the next pass exactly as today; the watermark is the offset after the last
complete line. Single-transaction-per-file atomicity is unchanged, so the existing
"Crash mid-file" guarantee still holds.

This is intentionally NOT chunked multi-commit: keeping one transaction per file avoids
changing the atomicity contract. A separate change can add per-window commits if a
single transaction's size ever becomes a problem (not observed today).

Oversized single line (eng-review D3, refined by outside-voice #1): cap the bytes read
for a single line at `maxLineBytes` (e.g. 16 MB). When a line reaches the cap without a
newline, keep scanning forward (discarding bytes, bounded memory) ONLY until its
terminating newline is actually observed; then count it in `unparsed_lines` and advance
the watermark past that newline. If EOF is reached before the newline, do NOT advance —
this is an in-progress append of a long line; leave the watermark before it and retry next
pass (the existing partial-line contract). Never advance to EOF on a newline-less tail, or
a later-arriving newline's prefix would be permanently dropped. This keeps memory bounded
while preserving append-safety.

## C1 — commit re-validation on stat error

In `commit()`, change the re-stat guard so a stat error is treated like a detected
change: return `errStaleSnapshot` (abort the pass, leave the watermark, retry next
pass) instead of falling through and committing. Size/mtime comparison is unchanged.

## Eng-review decisions (resolved)

- D2: empty stored `head_fingerprint` → skip head check + backfill (no forced reingest).
- D3: single-line cap `maxLineBytes` (16 MB); over-cap line counts as unparseable.
- Fingerprint window: keep 512 bytes for both head and tail.
- `unparsed_lines` semantics (revised by outside-voice #2): ACCUMULATE across incremental
  passes (each pass adds the bad lines it skipped), and reset to the run count ONLY on a
  full reingest / rewrite (`changeFull`). Per-run overwrite was rejected: incremental only
  reads past `last_byte_offset`, so a later clean append would overwrite the count with 0
  and make `doctor` go green while earlier skipped lines are still missing.

## Codex review outcomes (implementation)

- Tolerate a missing `unparsed_lines` column in `doctor` (read-only opens a pre-0005 DB).
- Accumulate `unparsed_lines` only on a STRICT offset advance (no double-count on an
  equal-offset duplicate commit).
- `classifyChange`: same-size + new-mtime is a rewrite → `changeFull` (an append always
  grows the file), closing the same-size-rewrite gap directly.
- Kept eng-review D2 (empty head → skip+backfill) over codex's "force a one-time full":
  the empty-head resume path is only reachable on size growth (a genuine append), and
  Claude session files are append-only, so the prefix is never rewritten on an append.
  Forcing full would re-index every file on upgrade (the cost D2 exists to avoid) to
  defend an edge this data cannot produce. Documented at the call site.

## Test strategy

TDD per fix in `internal/ingest/*_test.go`, plus a `db` migration test for 0005 and a
`doctor` test for the unparseable-line report. Reuse the existing `preCommitHook` seam
to exercise the stat-error abort.
