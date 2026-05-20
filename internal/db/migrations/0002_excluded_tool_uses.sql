-- Tracks clio's own MCP tool_use ids so that a tool_result arriving in a later
-- incremental ingest batch is still recognized and excluded (self-pollution).
CREATE TABLE IF NOT EXISTS excluded_tool_uses (
    tool_use_id TEXT PRIMARY KEY
);
