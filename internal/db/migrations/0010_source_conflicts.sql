CREATE TABLE IF NOT EXISTS source_conflicts (
    source_file        TEXT PRIMARY KEY,
    uuid               TEXT NOT NULL,
    seen_source        TEXT NOT NULL,
    conflicting_source TEXT NOT NULL,
    first_seen_at      INTEGER NOT NULL,
    last_seen_at       INTEGER NOT NULL
);
