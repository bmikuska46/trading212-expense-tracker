CREATE TABLE IF NOT EXISTS runtime_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

INSERT OR IGNORE INTO runtime_meta (id) VALUES (1);
