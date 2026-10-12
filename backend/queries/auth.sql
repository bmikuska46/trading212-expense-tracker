-- The owner row describes whose ledger a database holds. Sign-ins use users.
-- name: OwnerExists :one
SELECT count(*) FROM owner;

-- name: CreateOwner :exec
INSERT INTO owner (id, email, password_hash, timezone) VALUES (1, ?, '!', ?);

-- name: UpdateOwnerTimezone :exec
UPDATE owner SET timezone = ? WHERE id = 1;

-- name: GetTaxPercents :one
SELECT dividend_tax_percent, interest_tax_percent, cashback_tax_percent FROM owner WHERE id = 1;

-- name: UpdateTaxPercents :exec
UPDATE owner SET dividend_tax_percent = ?, interest_tax_percent = ?, cashback_tax_percent = ? WHERE id = 1;

-- name: CreateOwnerUser :exec
INSERT INTO users (id, email, password_hash) VALUES (1, ?, ?);

-- name: CreateUser :one
INSERT INTO users (email, password_hash, must_change_password) VALUES (?, ?, 1) RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ?, must_change_password = ? WHERE id = ?;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = ? AND id != 1;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, created_at, expires_at, ip, country, region, city, user_agent)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT sessions.expires_at, users.id AS user_id, users.email, users.must_change_password FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = ? AND sessions.expires_at > sqlc.arg(now);

-- name: ListUserSessions :many
SELECT token_hash, created_at, ip, country, region, city, user_agent FROM sessions
WHERE user_id = ? AND expires_at > sqlc.arg(now)
ORDER BY created_at DESC;

-- name: ExtendSession :exec
UPDATE sessions SET expires_at = ? WHERE token_hash = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = ?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ? AND token_hash != sqlc.arg(keep);

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= sqlc.arg(now);
