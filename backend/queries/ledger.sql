-- name: CreateAccount :one
INSERT INTO accounts (name, currency, integration_provider, external_account_id)
VALUES (?, ?, ?, ?) RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = ?;

-- name: CreateTransaction :one
INSERT INTO transactions (
    account_id, source_account_id, external_id, import_batch_id, occurred_at, local_date,
    kind, amount_minor, currency, minor_unit, eur_cents, payee_id, category_id,
    refund_of_transaction_id, notes, review_status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = ?;

-- name: FindTransactionBySourceID :one
SELECT * FROM transactions WHERE source_account_id = ? AND external_id = ?;

-- name: UpdateTransaction :one
UPDATE transactions SET
    account_id = ?, occurred_at = ?, local_date = ?, kind = ?, amount_minor = ?,
    currency = ?, minor_unit = ?, eur_cents = ?, payee_id = ?, category_id = ?,
    refund_of_transaction_id = ?, notes = ?, revision = revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND revision = sqlc.arg(expected_revision) AND deleted_at IS NULL RETURNING *;

-- name: DeleteTransaction :one
UPDATE transactions SET deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), revision = revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND revision = sqlc.arg(expected_revision) AND deleted_at IS NULL RETURNING *;

-- name: CreateImportedOriginal :exec
INSERT INTO imported_originals (
    transaction_id, amount_text, currency, timestamp_text, action, merchant_name,
    merchant_category, notes, fee_amount_text, fee_currency, raw_record_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetImportedOriginal :one
SELECT * FROM imported_originals WHERE transaction_id = ?;

-- name: ListTransactionEdits :many
SELECT * FROM transaction_edit_history WHERE transaction_id = ? ORDER BY id;

-- name: CreateTransactionConversion :exec
INSERT INTO transaction_conversions (
    transaction_id, original_amount_minor, original_currency, original_minor_unit,
    eur_cents, rate, rate_date, provider_policy, rounding_rule
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTransactionConversion :one
SELECT * FROM transaction_conversions WHERE transaction_id = ?;
