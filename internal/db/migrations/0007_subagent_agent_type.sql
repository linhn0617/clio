-- Subagent linking: record the subagent's type (e.g. general-purpose) on its
-- session row. parent_session (migration 0001) already carries the parent link.
ALTER TABLE sessions ADD COLUMN agent_type TEXT;
