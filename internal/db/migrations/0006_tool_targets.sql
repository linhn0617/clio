CREATE TABLE IF NOT EXISTS tool_targets (
    message_id   INTEGER NOT NULL,
    session_uuid TEXT NOT NULL,
    ts           INTEGER,
    kind         TEXT NOT NULL,
    value        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tool_targets_session ON tool_targets(session_uuid);
CREATE INDEX IF NOT EXISTS idx_tool_targets_kind_value ON tool_targets(kind, value);
CREATE INDEX IF NOT EXISTS idx_tool_targets_message ON tool_targets(message_id);
