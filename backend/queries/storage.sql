-- name: SetSetting :exec
INSERT INTO settings (key, value_json) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json,
updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: GetSetting :one
SELECT value_json FROM settings WHERE key = ?;

-- name: SetIntegration :exec
INSERT INTO integrations (provider, credentials_json, enabled) VALUES (?, ?, ?)
ON CONFLICT (provider) DO UPDATE SET credentials_json = excluded.credentials_json,
enabled = excluded.enabled, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: CreateImportBatch :one
INSERT INTO import_batches (source_account_id, filename, period_start, period_end)
VALUES (?, ?, ?, ?) RETURNING *;

-- name: GetImportBatch :one
SELECT * FROM import_batches WHERE id = ?;

-- name: StageRow :exec
INSERT INTO staging_rows (batch_id, row_number, raw_record_json, parsed_record_json, status, validation_errors_json)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListStagingRows :many
SELECT * FROM staging_rows WHERE batch_id = ? AND row_number > sqlc.arg(after_row)
ORDER BY row_number LIMIT sqlc.arg(page_size);
