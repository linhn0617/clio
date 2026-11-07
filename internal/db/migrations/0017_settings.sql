-- Settings stored in the index itself (the design rules out a config file and an
-- environment variable). First key: raw_retention, set by `clio config raw-retention`.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
