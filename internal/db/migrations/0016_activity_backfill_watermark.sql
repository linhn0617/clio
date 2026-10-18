-- No seed: an upgraded database must examine its existing history once.
CREATE TABLE activity_backfill_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    checked_through INTEGER NOT NULL
);
