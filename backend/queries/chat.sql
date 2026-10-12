-- name: ListChats :many
SELECT * FROM chats ORDER BY updated_at DESC, id DESC;

-- name: GetChat :one
SELECT * FROM chats WHERE id = ?;

-- name: CreateChat :one
INSERT INTO chats (title) VALUES (?) RETURNING *;

-- name: TouchChat :one
UPDATE chats SET updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ? RETURNING *;

-- name: RenameChat :one
UPDATE chats SET title = ? WHERE id = ? RETURNING *;

-- name: DeleteChat :execrows
DELETE FROM chats WHERE id = ?;

-- name: ListChatMessages :many
SELECT * FROM chat_messages WHERE chat_id = ? ORDER BY id;

-- name: LatestChatMessage :one
SELECT * FROM chat_messages WHERE chat_id = ? ORDER BY id DESC LIMIT 1;

-- name: CreateChatMessage :one
INSERT INTO chat_messages (chat_id, question, answer_json, interpretation_json) VALUES (?, ?, ?, ?) RETURNING *;
