-- name: ListAccounts :many
SELECT accounts.id, accounts.name, accounts.currency, accounts.integration_provider,
    accounts.external_account_id, accounts.archived, accounts.created_at,
    CAST(coalesce(sum(transactions.amount_minor), 0) AS INTEGER) AS balance_minor,
    CAST(coalesce(sum(transactions.eur_cents), 0) AS INTEGER) AS balance_eur_cents,
    CAST(count(transactions.id) AS INTEGER) AS transaction_count
FROM accounts
LEFT JOIN transactions ON transactions.account_id = accounts.id AND transactions.deleted_at IS NULL
GROUP BY accounts.id
ORDER BY accounts.archived, accounts.name COLLATE NOCASE, accounts.id;

-- name: FindIntegrationAccount :one
SELECT * FROM accounts WHERE integration_provider = ? AND external_account_id = ?;

-- name: RenameAccount :exec
UPDATE accounts SET name = ? WHERE id = ?;

-- name: GetUncategorizedCategory :one
SELECT id FROM categories WHERE normalized_name = 'uncategorized';

-- name: ListRecentTransactions :many
SELECT transactions.id, transactions.account_id, accounts.name AS account_name, transactions.local_date,
    transactions.kind, transactions.amount_minor, transactions.currency, transactions.minor_unit,
    transactions.eur_cents, transactions.notes, transactions.review_status
FROM transactions JOIN accounts ON accounts.id = transactions.account_id
WHERE transactions.deleted_at IS NULL
ORDER BY transactions.local_date DESC, transactions.id DESC
LIMIT sqlc.arg(page_size);
