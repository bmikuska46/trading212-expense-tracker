-- name: GetIncomeState :one
SELECT * FROM investment_income_state WHERE id = 1;

-- The first sync fixes the first imported day; later syncs keep it.
-- name: StartIncomeHistory :exec
UPDATE investment_income_state SET started_on = coalesce(started_on, sqlc.arg(started_on)) WHERE id = 1;

-- name: SaveDividendsSync :exec
UPDATE investment_income_state SET dividends_synced_at = ? WHERE id = 1;

-- name: SaveInterestSync :exec
UPDATE investment_income_state SET interest_synced_at = ? WHERE id = 1;

-- name: RecordIncomeHistory :exec
UPDATE investment_income_state SET history_attempt_at = ?, history_error = ?, history_next_at = ? WHERE id = 1;

-- name: RecordIncomeCashback :exec
UPDATE investment_income_state SET cashback_attempt_at = ?, cashback_error = ?, cashback_next_at = ? WHERE id = 1;

-- A report in progress moves on without changing the last outcome.
-- name: ScheduleIncomeCashback :exec
UPDATE investment_income_state SET cashback_next_at = ? WHERE id = 1;

-- The range is saved before the request, so a lost response is looked up
-- by its range next time.
-- name: RequestIncomeReport :exec
UPDATE investment_income_state SET report_id = NULL, report_from = sqlc.arg(report_from), report_to = sqlc.arg(report_to),
    report_requested_at = sqlc.arg(requested_at) WHERE id = 1;

-- name: SetIncomeReportID :exec
UPDATE investment_income_state SET report_id = ? WHERE id = 1;

-- name: PollIncomeReport :exec
UPDATE investment_income_state SET report_polled_at = ? WHERE id = 1;

-- name: FinishIncomeReport :exec
UPDATE investment_income_state SET cashback_through = report_to, report_id = NULL, report_from = NULL, report_to = NULL
WHERE id = 1;

-- name: DropIncomeReport :exec
UPDATE investment_income_state SET report_id = NULL, report_from = NULL, report_to = NULL WHERE id = 1;

-- name: RecordIncomeRates :exec
UPDATE investment_income_state SET rates_checked_at = ?, rates_error = ?, rates_next_at = ? WHERE id = 1;

-- Income is saved once; a later sync of the same record changes nothing.
-- name: SaveIncome :execrows
INSERT INTO investment_income (source_id, kind, paid_at, paid_on, currency, amount_minor, source_type, ticker, isin, name,
    quantity, per_share, received_currency, received_minor, imported_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_id) DO NOTHING;

-- name: ListIncome :many
SELECT * FROM investment_income WHERE paid_on >= sqlc.arg(since) AND paid_on < sqlc.arg(until)
ORDER BY paid_on, paid_at, id;

-- name: IncomeYears :many
SELECT DISTINCT CAST(substr(paid_on, 1, 4) AS TEXT) AS year FROM investment_income ORDER BY year DESC;

-- NBS rates are only needed for years with income in another currency.
-- name: ForeignIncomeYears :many
SELECT DISTINCT CAST(substr(paid_on, 1, 4) AS TEXT) AS year FROM investment_income WHERE currency != 'EUR' ORDER BY year;

-- name: IncomeMissingInformative :many
SELECT id, currency, amount_minor FROM investment_income WHERE informative_rate IS NULL ORDER BY id;

-- name: SetIncomeInformative :exec
UPDATE investment_income SET informative_eur_cents = ?, informative_rate = ?, informative_rate_date = ? WHERE id = ?;

-- name: SaveNBSRate :exec
INSERT INTO nbs_rates (currency, period, rate, fetched_at) VALUES (?, ?, ?, ?)
ON CONFLICT (currency, period) DO UPDATE SET rate = excluded.rate, fetched_at = excluded.fetched_at;

-- name: SaveNBSFile :exec
INSERT INTO nbs_rate_files (period, fetched_at, complete) VALUES (?, ?, ?)
ON CONFLICT (period) DO UPDATE SET fetched_at = excluded.fetched_at, complete = excluded.complete;

-- name: ListNBSFiles :many
SELECT * FROM nbs_rate_files ORDER BY period;

-- Periods sort as text, so a range from 'YYYY-12' of the previous year to
-- the next 'YYYY' holds a year's days, months, and annual average.
-- name: ListNBSRates :many
SELECT * FROM nbs_rates WHERE period >= sqlc.arg(since) AND period < sqlc.arg(until) ORDER BY currency, period;
