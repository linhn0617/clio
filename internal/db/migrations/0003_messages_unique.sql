-- Dedup any pre-existing duplicate (session_uuid, seq) rows, keeping the lowest
-- id. The AFTER DELETE trigger keeps messages_fts in sync.
DELETE FROM messages
WHERE id NOT IN (SELECT MIN(id) FROM messages GROUP BY session_uuid, seq);

-- Remove tool_calls orphaned by the dedup (no FK/cascade exists).
DELETE FROM tool_calls WHERE message_id NOT IN (SELECT id FROM messages);

-- Enforce idempotent message ingestion under concurrent writers.
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq
    ON messages(session_uuid, seq);
