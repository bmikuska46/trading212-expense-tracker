-- name: GetOwnerTimezone :one
SELECT timezone FROM owner WHERE id = 1;

-- name: EnsureMonthlyJob :exec
INSERT INTO scheduled_jobs (kind, source_account_id, period_key, timezone, period_start_utc, period_end_utc, next_attempt_at)
VALUES ('monthly_import', ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetScheduledJob :one
SELECT * FROM scheduled_jobs WHERE id = ?;

-- name: GetMonthlyJob :one
SELECT * FROM scheduled_jobs WHERE kind = 'monthly_import' AND source_account_id = ? AND period_key = ?;

-- Jobs run oldest period first, so one month finishes before the next starts.
-- name: NextMonthlyJob :one
SELECT * FROM scheduled_jobs
WHERE kind = 'monthly_import' AND source_account_id = ? AND state NOT IN ('completed', 'failed', 'canceled')
ORDER BY period_key LIMIT 1;

-- Completed months that are not imported through their end yet are synced
-- again every day.
-- name: ListPartialMonthlyJobs :many
SELECT * FROM scheduled_jobs
WHERE kind = 'monthly_import' AND source_account_id = ? AND period_key >= ? AND state = 'completed' AND
    (report_to IS NULL OR report_to < period_end_utc);

-- name: ListMonthlyJobs :many
SELECT * FROM scheduled_jobs WHERE kind = 'monthly_import' ORDER BY period_key DESC, id DESC LIMIT sqlc.arg(page_size);

-- Open jobs for another source account or for months before the earliest
-- automatic period are never run.
-- name: CancelStaleMonthlyJobs :many
UPDATE scheduled_jobs SET state = 'canceled', last_error_code = 'canceled', last_error_detail = sqlc.arg(detail),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE kind = 'monthly_import' AND state NOT IN ('completed', 'failed', 'canceled') AND
    (source_account_id != sqlc.arg(source_account_id) OR period_key < sqlc.arg(earliest))
RETURNING id;

-- name: SaveMonthlyJob :exec
UPDATE scheduled_jobs SET state = ?, report_id = ?, report_status = ?, report_requested_at = ?, report_from = ?, report_to = ?,
    batch_id = ?, retry_count = ?, next_attempt_at = ?, last_error_code = ?, last_error_detail = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: StartJobAttempt :one
UPDATE scheduled_jobs SET attempt_count = attempt_count + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING attempt_count;

-- An attempt interrupted by a stop never finished; its outcome is unknown.
-- name: AbandonMonthlyAttempts :exec
UPDATE job_attempts SET finished_at = ?, outcome = 'uncertain', error_code = 'interrupted',
    error_detail = 'The server stopped during this attempt'
WHERE finished_at IS NULL AND job_id IN (SELECT id FROM scheduled_jobs WHERE kind = 'monthly_import');

-- LastReportCall is when the monthly or investment income worker last
-- called a rate-limited Trading 212 endpoint, across restarts. Both share
-- the account's limits.
-- name: LastReportCall :one
SELECT CAST(coalesce(max(called_at), '') AS TEXT) AS started_at FROM (
    SELECT job_attempts.started_at AS called_at FROM job_attempts
    JOIN scheduled_jobs ON scheduled_jobs.id = job_attempts.job_id
    WHERE scheduled_jobs.kind = 'monthly_import' AND job_attempts.stage = sqlc.arg(stage)
    UNION ALL
    SELECT CASE sqlc.arg(stage) WHEN 'request' THEN report_requested_at WHEN 'poll' THEN report_polled_at END
    FROM investment_income_state
);

-- A rerun reports from report_from, or from the month's start when it is NULL.
-- name: RerunMonthlyJob :execrows
UPDATE scheduled_jobs SET state = 'pending', report_id = NULL, report_status = NULL, report_requested_at = NULL,
    report_from = sqlc.narg(report_from), report_to = NULL, batch_id = NULL, retry_count = 0,
    next_attempt_at = sqlc.arg(next_attempt_at), last_error_code = NULL, last_error_detail = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = sqlc.arg(id) AND state IN ('completed', 'failed', 'canceled');

-- A monthly import that stopped for the owner's attention completes when the
-- owner commits its batch.
-- name: CompleteJobsForBatch :many
UPDATE scheduled_jobs SET state = 'completed', last_error_code = NULL, last_error_detail = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE batch_id = ? AND state = 'failed'
RETURNING id;

-- name: JobForBatch :one
SELECT * FROM scheduled_jobs WHERE batch_id = ? ORDER BY id DESC LIMIT 1;

-- name: LatestJobAttempts :many
SELECT * FROM job_attempts WHERE job_id = ? ORDER BY attempt_number DESC LIMIT sqlc.arg(page_size);

-- A removed import leaves its scheduled job and failure notification in
-- place without the batch.
-- name: DetachJobBatch :exec
UPDATE scheduled_jobs SET batch_id = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE batch_id = ?;

-- name: DetachFailureBatch :exec
UPDATE notification_events SET batch_id = NULL WHERE batch_id = ? AND kind = 'import_failure' AND job_id IS NOT NULL;

-- The newest batch of a month's report that added transactions.
-- name: PreviousMonthlyBatch :one
SELECT id FROM import_batches
WHERE source_account_id = ? AND filename = ? AND status = 'committed' AND inserted_count > 0
ORDER BY id DESC LIMIT 1;

-- name: DeleteEmptyBatch :execrows
DELETE FROM import_batches WHERE import_batches.id = ? AND status = 'committed' AND inserted_count = 0
AND NOT EXISTS (SELECT 1 FROM transactions WHERE transactions.import_batch_id = import_batches.id)
AND NOT EXISTS (SELECT 1 FROM source_conflicts WHERE source_conflicts.batch_id = import_batches.id)
AND NOT EXISTS (SELECT 1 FROM scheduled_jobs WHERE scheduled_jobs.batch_id = import_batches.id)
AND NOT EXISTS (SELECT 1 FROM notification_events WHERE notification_events.batch_id = import_batches.id);

-- Rows the owner excluded or corrected in a committed import into the account.
-- A rerun requests the month from its start, so it leaves these out instead
-- of asking about them again.
-- name: HandledReportRows :many
SELECT DISTINCT staging_rows.raw_record_json FROM staging_rows
JOIN import_batches ON import_batches.id = staging_rows.batch_id
WHERE import_batches.source_account_id = ? AND import_batches.status = 'committed' AND
    (staging_rows.excluded_by_owner = 1 OR staging_rows.corrected_record_json IS NOT NULL);
