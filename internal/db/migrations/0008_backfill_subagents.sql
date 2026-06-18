-- One-time backfill for subagent linking: clear the ingest watermark for subagent
-- transcripts (files under a subagents/ directory) so the next index re-ingests
-- them in place and populates parent_session / agent_type on the existing (orphan)
-- session rows. Idempotent; matches both POSIX and Windows path separators.
DELETE FROM ingest_state
WHERE source_file LIKE '%/subagents/%'
   OR source_file LIKE '%\subagents\%';
