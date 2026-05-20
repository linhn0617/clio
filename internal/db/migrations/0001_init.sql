CREATE TABLE IF NOT EXISTS sessions (
    uuid           TEXT PRIMARY KEY,
    project_path   TEXT,
    source_file    TEXT NOT NULL,
    started_at     INTEGER,
    ended_at       INTEGER,
    turn_count     INTEGER NOT NULL DEFAULT 0,
    title          TEXT,
    parent_session TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_path);
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at);

CREATE TABLE IF NOT EXISTS messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_uuid TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    ts           INTEGER,
    role         TEXT NOT NULL,
    content      TEXT NOT NULL,
    raw_json     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_uuid);
CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts);
CREATE INDEX IF NOT EXISTS idx_messages_role ON messages(role);

-- External-content FTS5 over messages.content; rowid == messages.id.
-- Triggers below keep it in sync automatically (canonical external-content pattern).
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    tokenize = 'trigram',
    content = 'messages',
    content_rowid = 'id'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TABLE IF NOT EXISTS tool_calls (
    message_id     INTEGER NOT NULL,
    tool_name      TEXT NOT NULL,
    params_summary TEXT
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_message ON tool_calls(message_id);

CREATE TABLE IF NOT EXISTS ingest_state (
    source_file      TEXT PRIMARY KEY,
    last_size        INTEGER NOT NULL,
    last_mtime       INTEGER NOT NULL,
    last_byte_offset INTEGER NOT NULL,
    tail_fingerprint TEXT NOT NULL,
    last_ingested_at INTEGER NOT NULL
);
