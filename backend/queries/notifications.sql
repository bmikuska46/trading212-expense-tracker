-- name: CreateNotificationEvent :one
INSERT INTO notification_events (batch_id, job_id, kind, payload_json, idempotency_key, next_attempt_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetJobFailure :one
SELECT * FROM notification_events WHERE kind = 'import_failure' AND job_id = ?;

-- A repeated failure of a job whose failure is still unresolved updates the
-- shown cause without sending another email.
-- name: UpdateJobFailure :exec
UPDATE notification_events SET batch_id = ?, payload_json = ?, dismissed_at = NULL WHERE id = ?;

-- A job that fails again after its failure was resolved alerts again, as a
-- new delivery with a new idempotency key.
-- name: RearmJobFailure :exec
UPDATE notification_events SET batch_id = ?, payload_json = ?, idempotency_key = ?, delivery_state = 'pending',
    next_attempt_at = ?, first_attempt_at = NULL, email_id = NULL, last_error_code = NULL, last_error_detail = NULL,
    dismissed_at = NULL
WHERE id = ?;

-- name: GetNotificationEvent :one
SELECT * FROM notification_events WHERE id = ?;

-- name: ListActiveNotifications :many
SELECT * FROM notification_events WHERE dismissed_at IS NULL ORDER BY id DESC LIMIT sqlc.arg(page_size);

-- name: CountActiveNotifications :one
SELECT count(*) FROM notification_events WHERE dismissed_at IS NULL;

-- name: ListRecentNotifications :many
SELECT * FROM notification_events ORDER BY id DESC LIMIT sqlc.arg(page_size);

-- name: DueNotification :one
SELECT * FROM notification_events
WHERE delivery_state = 'pending' AND dismissed_at IS NULL AND next_attempt_at <= sqlc.arg(now)
ORDER BY next_attempt_at, id LIMIT 1;

-- name: NextNotificationAt :one
SELECT CAST(coalesce(min(next_attempt_at), '') AS TEXT) AS next_attempt_at FROM notification_events
WHERE delivery_state = 'pending' AND dismissed_at IS NULL;

-- name: StartNotificationAttempt :one
UPDATE notification_events SET attempt_count = attempt_count + 1, delivery_state = 'sending',
    first_attempt_at = coalesce(first_attempt_at, sqlc.arg(started_at)), payload_json = sqlc.arg(payload_json)
WHERE id = sqlc.arg(id) AND delivery_state = 'pending'
RETURNING attempt_count;

-- name: CreateNotificationAttempt :one
INSERT INTO notification_attempts (event_id, attempt_number, started_at) VALUES (?, ?, ?) RETURNING id;

-- name: FinishNotificationAttempt :exec
UPDATE notification_attempts SET finished_at = ?, outcome = ?, email_id = ?, error_code = ?, error_detail = ? WHERE id = ?;

-- name: FinishNotification :exec
UPDATE notification_events SET delivery_state = ?, next_attempt_at = ?, email_id = ?, last_error_code = ?, last_error_detail = ?
WHERE id = ?;

-- An attempt interrupted by a stop may or may not have reached Resend. The
-- event is retried with the same idempotency key while Resend remembers it.
-- name: AbandonNotificationAttempts :exec
UPDATE notification_attempts SET finished_at = ?, outcome = 'uncertain', error_code = 'interrupted',
    error_detail = 'The server stopped during this attempt'
WHERE finished_at IS NULL;

-- name: ResumeSendingNotifications :exec
UPDATE notification_events SET delivery_state = 'pending' WHERE delivery_state = 'sending';

-- name: CountUncertainAttempts :one
SELECT count(*) FROM notification_attempts WHERE event_id = ? AND outcome = 'uncertain';

-- name: DismissNotification :execrows
UPDATE notification_events SET dismissed_at = ? WHERE id = ? AND kind = 'import_failure' AND dismissed_at IS NULL;

-- name: DismissBatchReview :exec
UPDATE notification_events SET dismissed_at = ? WHERE kind = 'import_review' AND batch_id = ? AND dismissed_at IS NULL;

-- name: DismissJobFailure :exec
UPDATE notification_events SET dismissed_at = ? WHERE kind = 'import_failure' AND job_id = ? AND dismissed_at IS NULL;

-- Sending again is an explicit owner decision after a failed or uncertain
-- delivery, so it uses a new idempotency key and renders the email again
-- with the current settings.
-- name: ResendNotification :execrows
UPDATE notification_events SET idempotency_key = ?, payload_json = ?, delivery_state = 'pending', next_attempt_at = ?,
    first_attempt_at = NULL, email_id = NULL, last_error_code = NULL, last_error_detail = NULL
WHERE id = ? AND delivery_state IN ('failed', 'uncertain');

-- name: ConfirmBatch :execrows
UPDATE import_batches SET review_status = 'confirmed', confirmed_at = ? WHERE id = ? AND review_status = 'needs_review';
