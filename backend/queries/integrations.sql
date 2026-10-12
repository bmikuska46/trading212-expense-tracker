-- name: ListIntegrations :many
SELECT * FROM integrations ORDER BY provider;

-- name: GetIntegration :one
SELECT * FROM integrations WHERE provider = ?;

-- name: SaveIntegration :exec
INSERT INTO integrations (provider, credentials_json, enabled, earliest_automatic_period, source_account_id, check_json, checked_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider) DO UPDATE SET credentials_json = excluded.credentials_json,
    enabled = excluded.enabled, earliest_automatic_period = excluded.earliest_automatic_period,
    source_account_id = excluded.source_account_id, check_json = excluded.check_json,
    checked_at = excluded.checked_at, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: RecordIntegrationCheck :exec
UPDATE integrations SET check_json = ?, checked_at = ? WHERE provider = ?;

-- name: GetNotificationConfig :one
SELECT * FROM notification_config WHERE id = 1;

-- name: SaveNotificationConfig :exec
INSERT INTO notification_config (id, provider, sender, recipient, application_url, enabled)
VALUES (1, 'resend', ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET sender = excluded.sender, recipient = excluded.recipient,
    application_url = excluded.application_url, enabled = excluded.enabled;

-- name: DisableNotifications :exec
UPDATE notification_config SET enabled = 0 WHERE id = 1;
