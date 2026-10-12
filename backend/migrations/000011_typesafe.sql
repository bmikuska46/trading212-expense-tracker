-- TypeSafe Jev replaces OpenAI for mapping suggestions. An OpenAI key cannot
-- call TypeSafe, so its row is dropped rather than carried over.
-- SQLite cannot change a CHECK in place. Accounts and notification_config
-- reference integrations, so foreign keys are deferred while the table is
-- rebuilt; inserting the rows again satisfies them before the commit.
PRAGMA defer_foreign_keys = ON;

CREATE TABLE integrations_previous (
    provider TEXT NOT NULL,
    credentials_json TEXT NOT NULL,
    enabled INTEGER NOT NULL,
    earliest_automatic_period TEXT,
    updated_at TEXT NOT NULL,
    source_account_id INTEGER,
    check_json TEXT,
    checked_at TEXT
) STRICT;
INSERT INTO integrations_previous
SELECT provider, credentials_json, enabled, earliest_automatic_period, updated_at, source_account_id, check_json, checked_at
FROM integrations WHERE provider != 'openai';
DROP TABLE integrations;

CREATE TABLE integrations (
    provider TEXT PRIMARY KEY CHECK (provider IN ('trading212', 'typesafe', 'resend')),
    credentials_json TEXT NOT NULL CHECK (json_valid(credentials_json)),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    earliest_automatic_period TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    source_account_id INTEGER REFERENCES accounts(id),
    check_json TEXT CHECK (check_json IS NULL OR json_valid(check_json)),
    checked_at TEXT
) STRICT;
INSERT INTO integrations SELECT * FROM integrations_previous;
DROP TABLE integrations_previous;
