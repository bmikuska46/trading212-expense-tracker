-- name: SetBatchFormat :exec
UPDATE import_batches SET header_json = ?, format_fingerprint = ? WHERE id = ?;

-- name: FinishStaging :exec
UPDATE import_batches SET staged_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), row_count = ?
WHERE id = ? AND status = 'staging';

-- name: FailStaging :exec
UPDATE import_batches SET status = 'failed', row_count = 0, error_count = 1, validation_errors_json = ?
WHERE id = ? AND staged_at IS NULL;

-- name: DeleteStagingRows :exec
DELETE FROM staging_rows WHERE batch_id = ?;

-- name: RecoverInterruptedStaging :many
UPDATE import_batches SET status = 'failed', row_count = 0, error_count = 1, validation_errors_json = ?
WHERE status = 'staging' AND staged_at IS NULL RETURNING id;

-- name: LatestMapping :one
SELECT * FROM csv_mappings WHERE source = ? AND format_fingerprint = ?
ORDER BY version DESC LIMIT 1;

-- name: GetMapping :one
SELECT * FROM csv_mappings WHERE id = ?;

-- name: CreateMapping :one
INSERT INTO csv_mappings (source, format_fingerprint, version, mapping_json)
VALUES (?, ?, ?, ?) RETURNING *;

-- name: SetBatchMapping :exec
UPDATE import_batches SET mapping_id = ?, mapping_snapshot_json = ?, status = 'staging',
    period_start = NULL, period_end = NULL, excluded_count = 0, duplicate_count = 0, error_count = 0,
    conflict_count = 0, validation_errors_json = '[]', unknown_actions_json = '[]'
WHERE id = ? AND status != 'committed';

-- name: InvalidateValidation :exec
UPDATE import_batches SET status = 'staging' WHERE id = ? AND status IN ('validating', 'failed') AND staged_at IS NOT NULL;

-- name: UpdateStagingRow :exec
UPDATE staging_rows SET parsed_record_json = ?, status = ?, validation_errors_json = ?, external_id = ?
WHERE batch_id = ? AND row_number = ?;

-- name: EarlierStagedID :one
SELECT row_number, raw_record_json, corrected_record_json FROM staging_rows
WHERE batch_id = ? AND external_id = ? AND row_number < sqlc.arg(before_row)
ORDER BY row_number LIMIT 1;

-- name: GetStagingRow :one
SELECT * FROM staging_rows WHERE batch_id = ? AND row_number = ?;

-- name: CorrectStagingRow :exec
UPDATE staging_rows SET excluded_by_owner = ?, corrected_record_json = ?
WHERE batch_id = ? AND row_number = ?;

-- name: ListStagingRowsByStatus :many
SELECT * FROM staging_rows WHERE batch_id = ? AND (sqlc.arg(status) = '' OR status = sqlc.arg(status))
AND row_number > sqlc.arg(after_row) ORDER BY row_number LIMIT sqlc.arg(page_size);

-- name: CountStagingRows :many
SELECT status, count(*) AS row_count FROM staging_rows WHERE batch_id = ? GROUP BY status;

-- name: SetValidationResult :exec
UPDATE import_batches SET status = ?, period_start = ?, period_end = ?, excluded_count = ?,
    duplicate_count = ?, error_count = ?, conflict_count = ?, validation_errors_json = ?, unknown_actions_json = ?
WHERE id = ? AND status != 'committed';

-- name: CommitBatch :exec
UPDATE import_batches SET status = 'committed', inserted_count = sqlc.arg(inserted_count),
    review_status = CASE WHEN sqlc.arg(inserted_count) > 0 THEN 'needs_review' ELSE 'not_required' END,
    committed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = sqlc.arg(id) AND status = 'validating';

-- ListImportBatches loads one page of imports in any order; the handler picks
-- and sorts the page.
-- name: ListImportBatches :many
SELECT import_batches.*, accounts.name AS account_name FROM import_batches
JOIN accounts ON accounts.id = import_batches.source_account_id
WHERE import_batches.id IN (sqlc.slice(ids));

-- name: CountImportBatches :one
SELECT count(*) FROM import_batches;

-- name: DeleteImportBatch :execrows
DELETE FROM import_batches WHERE import_batches.id = ? AND import_batches.status != 'committed'
AND NOT EXISTS (SELECT 1 FROM transactions WHERE transactions.import_batch_id = import_batches.id)
AND NOT EXISTS (SELECT 1 FROM scheduled_jobs WHERE scheduled_jobs.batch_id = import_batches.id)
AND NOT EXISTS (SELECT 1 FROM notification_events WHERE notification_events.batch_id = import_batches.id);

-- name: RecordImportedOriginal :exec
INSERT INTO imported_originals (
    transaction_id, amount_text, currency, timestamp_text, action, merchant_name,
    merchant_category, notes, fee_amount_text, fee_currency, raw_record_json, amount_minor, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CreateSourceConflict :exec
INSERT INTO source_conflicts (transaction_id, batch_id, row_number, incoming_record_json) VALUES (?, ?, ?, ?);

-- name: SourceConflictSeen :one
SELECT EXISTS (SELECT 1 FROM source_conflicts WHERE transaction_id = ? AND incoming_record_json = ?);

-- name: CountEarlierIDs :one
SELECT count(*) FROM staging_rows WHERE batch_id = ? AND external_id >= sqlc.arg(low)
AND external_id < sqlc.arg(high) AND row_number < sqlc.arg(before_row);
