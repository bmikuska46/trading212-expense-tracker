-- Chats keep the owner's questions to TypeSafe Jev and the answers as they
-- were shown, so earlier conversations read the same later. The
-- interpretation lets a follow-up question reuse what the previous one asked.
CREATE TABLE chats (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;
CREATE INDEX chats_updated ON chats(updated_at, id);

CREATE TABLE chat_messages (
    id INTEGER PRIMARY KEY,
    chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    question TEXT NOT NULL CHECK (length(trim(question)) > 0),
    answer_json TEXT NOT NULL CHECK (json_valid(answer_json)),
    interpretation_json TEXT NOT NULL CHECK (json_valid(interpretation_json)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;
CREATE INDEX chat_messages_chat ON chat_messages(chat_id, id);
