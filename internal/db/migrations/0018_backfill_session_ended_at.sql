-- Repair sessions.ended_at that the old incremental upsert cleared: it ran
-- MAX(COALESCE(ended_at,0), ?) with a NULL bind for a batch without a timestamp,
-- and SQLite's multi-argument MAX returns NULL when any argument is NULL. The
-- restored value is the session's newest message timestamp — messages.ts and
-- sessions.ended_at are both the line timestamp in unix seconds (parseTS), for
-- every source. A lower bound when the newest timestamped line produced no
-- message, which is still far better than falling back to started_at.
-- Idempotent: only rows still NULL with at least one timestamped message change.
UPDATE sessions
SET ended_at = (SELECT MAX(m.ts) FROM messages m WHERE m.session_uuid = sessions.uuid)
WHERE ended_at IS NULL
  AND EXISTS (SELECT 1 FROM messages m WHERE m.session_uuid = sessions.uuid AND m.ts IS NOT NULL);
