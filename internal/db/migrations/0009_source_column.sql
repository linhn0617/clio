ALTER TABLE sessions ADD COLUMN source TEXT;
UPDATE sessions SET source = 'claude-code' WHERE source IS NULL OR source = '';
CREATE INDEX IF NOT EXISTS idx_sessions_source ON sessions(source);
