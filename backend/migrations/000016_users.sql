-- Several people can use the app. Everyone signs in through users in this
-- database. The owner is user 1, whose ledger is this database; the owner adds
-- the other users, and each of their ledgers is a database file of its own.
-- A user the owner adds, or whose password the owner resets, signs in with a
-- temporary password and must choose their own before anything else.
-- AUTOINCREMENT never reuses the id of a removed user, so a new user can
-- never be given a ledger file that belonged to someone else.
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK (length(trim(email)) > 0),
    password_hash TEXT NOT NULL CHECK (length(password_hash) > 0),
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;
INSERT INTO users (id, email, password_hash, created_at) SELECT id, email, password_hash, created_at FROM owner;

-- The owner table now only describes whose ledger a database holds, with the
-- timezone its dates follow. The password lives in users alone.
UPDATE owner SET password_hash = '!';

CREATE TABLE sessions_new (
    token_hash BLOB PRIMARY KEY CHECK (length(token_hash) = 32),
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;
INSERT INTO sessions_new (token_hash, user_id, created_at, expires_at)
SELECT token_hash, owner_id, created_at, expires_at FROM sessions;
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE INDEX sessions_user ON sessions(user_id);
