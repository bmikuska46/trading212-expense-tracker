-- name: SaveRate :exec
INSERT INTO exchange_rates (base_currency, quote_currency, publication_date, rate, provider_policy, fetched_at, confirmed_through)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (base_currency, quote_currency, publication_date) DO UPDATE SET rate = excluded.rate,
    provider_policy = excluded.provider_policy, fetched_at = excluded.fetched_at,
    confirmed_through = max(coalesce(exchange_rates.confirmed_through, exchange_rates.publication_date), excluded.confirmed_through);

-- Ledger amounts convert to EUR, so lookups read the EUR-base rates.
-- name: RateAtOrBefore :one
SELECT * FROM exchange_rates WHERE base_currency = 'EUR' AND quote_currency = ? AND publication_date <= sqlc.arg(local_date)
ORDER BY publication_date DESC LIMIT 1;

-- name: ListRatesBefore :many
SELECT base_currency, quote_currency, publication_date FROM exchange_rates WHERE publication_date < sqlc.arg(before)
ORDER BY base_currency, quote_currency, publication_date;

-- name: DeleteRate :exec
DELETE FROM exchange_rates WHERE base_currency = ? AND quote_currency = ? AND publication_date = ?;

-- name: RateSummary :one
SELECT count(DISTINCT CASE WHEN base_currency = 'EUR' THEN quote_currency END) AS currencies,
    CAST(coalesce(group_concat(DISTINCT base_currency), '') AS TEXT) AS bases, CAST(coalesce(max(publication_date), '') AS TEXT) AS latest_publication,
    CAST(coalesce(max(fetched_at), '') AS TEXT) AS last_fetched_at
FROM exchange_rates;

-- Staged batches that are not committed, or committed but not yet confirmed,
-- still need the rates for their months.
-- name: PendingImportPeriods :many
SELECT id, period_start, period_end FROM import_batches
WHERE staged_at IS NOT NULL AND (status != 'committed' OR review_status = 'needs_review');

-- name: EnsureRatesJob :exec
INSERT INTO scheduled_jobs (kind, period_key, timezone, next_attempt_at) VALUES ('daily_rates', ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetRatesJob :one
SELECT * FROM scheduled_jobs WHERE kind = 'daily_rates' AND period_key = ?;

-- name: CloseMissedRatesJobs :exec
UPDATE scheduled_jobs SET state = 'failed', last_error_code = 'missed', last_error_detail = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE kind = 'daily_rates' AND period_key < sqlc.arg(today) AND state NOT IN ('completed', 'failed', 'canceled');

-- An attempt interrupted by a stop never finished; its outcome is unknown.
-- name: AbandonRatesAttempts :exec
UPDATE job_attempts SET finished_at = ?, outcome = 'uncertain', error_code = 'interrupted',
    error_detail = 'The server stopped during this attempt'
WHERE finished_at IS NULL AND job_id IN (SELECT id FROM scheduled_jobs WHERE kind = 'daily_rates');

-- name: StartRatesAttempt :one
UPDATE scheduled_jobs SET attempt_count = attempt_count + 1,
    state = CASE WHEN state = 'completed' THEN state ELSE 'requesting' END,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING attempt_count;

-- name: CreateJobAttempt :one
INSERT INTO job_attempts (job_id, attempt_number, stage, started_at) VALUES (?, ?, ?, ?) RETURNING id;

-- name: FinishJobAttempt :exec
UPDATE job_attempts SET finished_at = ?, outcome = ?, error_code = ?, error_detail = ? WHERE id = ?;

-- name: FinishRatesJob :exec
UPDATE scheduled_jobs SET state = ?, next_attempt_at = ?, last_error_code = ?, last_error_detail = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: LatestRatesAttempts :many
SELECT job_attempts.* FROM job_attempts JOIN scheduled_jobs ON scheduled_jobs.id = job_attempts.job_id
WHERE scheduled_jobs.kind = 'daily_rates' ORDER BY job_attempts.id DESC LIMIT sqlc.arg(page_size);

-- Every account currency is fetched as a base, archived accounts included.
-- name: ListAccountCurrencies :many
SELECT DISTINCT currency FROM accounts ORDER BY currency;
