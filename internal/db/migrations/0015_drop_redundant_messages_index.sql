-- idx_messages_session (0001) indexes messages(session_uuid). The unique index
-- idx_messages_session_seq (0003) is on (session_uuid, seq), so its prefix serves
-- every session_uuid lookup and ordered/range read the single-column index could.
-- Keeping both only costs an extra B-tree write per inserted message.
DROP INDEX IF EXISTS idx_messages_session;
